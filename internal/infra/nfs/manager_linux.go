package nfs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

type image struct {
	name     string
	export   int
	size     int64
	deleting bool
	mounted  bool
}
type manager struct {
	mu             sync.Mutex
	root           *os.Root
	mounts, config string
	volumes        map[string]*image
	legacy         bool
}

func newManager(backing, mounts, config string) (*manager, error) {
	if err := prepareStorageDirs(backing, mounts); err != nil {
		return nil, err
	}

	old, err := os.ReadDir(filepath.Join(backing, "volumes"))
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(filepath.Join(backing, "images"))
	if err != nil {
		return nil, err
	}
	m := &manager{root: root, mounts: mounts, config: config, volumes: map[string]*image{}, legacy: len(old) > 0}
	entries, err := os.ReadDir(root.Name())
	if err != nil {
		_ = root.Close()
		return nil, err
	}
	used := map[int]bool{}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".creating-") {
			continue
		}
		n, id, ok := strings.Cut(entry.Name(), "-")
		id = strings.TrimSuffix(id, ".img")
		number, e := strconv.Atoi(n)
		if !ok || e != nil || number < 101 || number > 65535 || !volumeID.MatchString(id) || !strings.HasSuffix(entry.Name(), ".img") || used[number] || m.volumes[id] != nil {
			_ = m.close()
			return nil, fmt.Errorf("invalid backing image %s", entry.Name())
		}
		st, e := root.Lstat(entry.Name())
		if e != nil || !st.Mode().IsRegular() {
			_ = m.close()
			return nil, fmt.Errorf("unsafe backing image %s", entry.Name())
		}
		if m.legacy {
			_ = m.close()
			return nil, fmt.Errorf("legacy and bounded storage cannot share an export")
		}
		if e := m.mount(id, entry.Name()); e != nil {
			_ = m.close()
			return nil, e
		}
		m.volumes[id] = &image{name: entry.Name(), export: number, size: st.Size(), mounted: true}
		used[number] = true
	}
	if err := m.reload(); err != nil {
		_ = m.close()
		return nil, err
	}
	return m, nil
}

func (m *manager) Ensure(ctx context.Context, id string, size int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if !volumeID.MatchString(id) || size < 16<<20 || size > 1<<40 || size%(1<<20) != 0 {
		return fmt.Errorf("invalid volume identifier or capacity")
	}
	if m.legacy {
		return fmt.Errorf("legacy storage requires an explicit offline migration before new allocations")
	}
	if v := m.volumes[id]; v != nil {
		if v.size != size || v.deleting {
			return fmt.Errorf("volume capacity or lifecycle conflict")
		}
		if !v.mounted {
			if err := m.mount(id, v.name); err != nil {
				return err
			}
			v.mounted = true
		}
		return m.reload()
	}
	used := map[int]bool{}
	for _, v := range m.volumes {
		used[v.export] = true
	}
	n := 101
	for used[n] {
		n++
	}
	if n > 65535 {
		return fmt.Errorf("NFS export identifiers exhausted")
	}
	if err := m.createImage(id, size, n); err != nil {
		return err
	}
	if err := m.mount(id, m.volumes[id].name); err != nil {
		return err
	}
	m.volumes[id].mounted = true
	return m.reload()
}

func (m *manager) createImage(id string, size int64, n int) error {
	d, err := m.root.Open(".")
	if err != nil {
		return err
	}
	var stat unix.Statfs_t
	err = unix.Fstatfs(int(d.Fd()), &stat)
	_ = d.Close()
	if err != nil {
		return err
	}
	if stat.Bsize <= 0 {
		return fmt.Errorf("invalid backing storage block size")
	}
	// #nosec G115 -- Ensure validates 16Mi <= size <= 1Ti before creating an image.
	needed := uint64(size) + (8 << 20)
	block := uint64(stat.Bsize)
	if (needed+block-1)/block > stat.Bavail {
		return fmt.Errorf("insufficient free backing storage for volume and metadata")
	}

	name := fmt.Sprintf("%05d-%s.img", n, id)
	// Incomplete images are never published; retries replace only this private temporary file.
	temp := ".creating-" + id
	if st, e := m.root.Lstat(temp); e == nil {
		if !st.Mode().IsRegular() {
			return fmt.Errorf("unsafe temporary image")
		}
		if e = m.root.Remove(temp); e != nil {
			return e
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		return e
	}
	f, err := m.root.OpenFile(temp, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	err = formatImage(f, size)
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = m.root.Rename(temp, name); err != nil {
		return err
	}
	m.volumes[id] = &image{name: name, export: n, size: size}
	d, err = m.root.Open(".")
	if err != nil {
		return err
	}
	err = d.Sync()
	_ = d.Close()
	if err != nil {
		return err
	}
	return nil
}

func (m *manager) Delete(ctx context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if !volumeID.MatchString(id) {
		return fmt.Errorf("invalid volume identifier")
	}
	v := m.volumes[id]
	if v == nil {
		temp := ".creating-" + id
		st, err := m.root.Lstat(temp)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if !st.Mode().IsRegular() {
			return fmt.Errorf("unsafe temporary image")
		}
		if err := m.root.Remove(temp); err != nil {
			return err
		}
		d, err := m.root.Open(".")
		if err != nil {
			return err
		}
		defer func() { _ = d.Close() }()
		return d.Sync()
	}

	v.deleting = true
	if err := m.reload(); err != nil {
		return err
	}
	if err := unmountReleased(ctx, filepath.Join(m.mounts, id)); err != nil {
		return err
	}

	if err := m.root.Remove(v.name); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	// #nosec G703 -- id passed the anchored volumeID whitelist before this join.
	if err := os.Remove(filepath.Join(m.mounts, id)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	d, err := m.root.Open(".")
	if err != nil {
		return err
	}
	err = d.Sync()
	_ = d.Close()
	if err != nil {
		return err
	}
	delete(m.volumes, id)
	return nil
}

func (m *manager) mount(id, name string) error {
	path := filepath.Join(m.mounts, id)
	// #nosec G301 G703 -- NFS workload UIDs need to traverse mount directories.
	if err := os.MkdirAll(path, 0o755); err != nil {
		return err
	}
	// #nosec G703 -- mount paths contain only internally validated volume identifiers.
	st, err := os.Lstat(path)
	if err != nil || !st.IsDir() {
		return fmt.Errorf("unsafe mount point")
	}
	f, err := m.root.OpenFile(name, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	// #nosec G115 -- fixed device major/minor fit the Linux device number.
	if err := unix.Mknod("/dev/loop-control", unix.S_IFCHR|0o600, int(unix.Mkdev(10, 237))); err != nil && !errors.Is(err, unix.EEXIST) {
		return err
	}
	ctl, err := unix.Open("/dev/loop-control", unix.O_RDWR|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(ctl) }()
	for attempt := 0; attempt < 16; attempt++ {
		n, err := unix.IoctlRetInt(ctl, unix.LOOP_CTL_GET_FREE)
		if err != nil {
			return err
		}
		if n < 0 || n > 1048575 {
			return fmt.Errorf("invalid loop device number")
		}
		dev := fmt.Sprintf("/dev/loop%d", n)
		// #nosec G115 -- loop minor is bounded above, fixed major fits Linux dev_t.
		if err := unix.Mknod(dev, unix.S_IFBLK|0o600, int(unix.Mkdev(7, uint32(n)))); err != nil && !errors.Is(err, unix.EEXIST) {
			return err
		}
		fd, err := unix.Open(dev, unix.O_RDWR|unix.O_CLOEXEC, 0)
		if err != nil {
			return err
		}
		if f.Fd() > 0xffffffff {
			_ = unix.Close(fd)
			return fmt.Errorf("invalid loop backing descriptor")
		}
		// #nosec G115 -- the descriptor is checked against uint32 bounds above.
		err = unix.IoctlLoopConfigure(fd, &unix.LoopConfig{Fd: uint32(f.Fd()), Info: unix.LoopInfo64{Flags: unix.LO_FLAGS_AUTOCLEAR}})
		if errors.Is(err, unix.EBUSY) {
			_ = unix.Close(fd)
			continue
		}
		if err != nil {
			_ = unix.Close(fd)
			return err
		}
		err = unix.Mount(dev, path, "ext4", unix.MS_NODEV|unix.MS_NOSUID, "")
		_ = unix.Close(fd)
		if err != nil {
			return err
		}
		// #nosec G302 G703 -- validated volume IDs; volumes support arbitrary workload UIDs.
		return os.Chmod(path, 0o777)
	}
	return fmt.Errorf("loop devices busy")
}

func (m *manager) reload() error {
	exports := map[string]int{}
	for id, v := range m.volumes {
		if !v.deleting {
			exports[id] = v.export
		}
	}
	config, err := renderExports(exports, m.legacy)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(m.config), ".exports-")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	_, err = f.WriteString(config)
	if err == nil {
		err = f.Sync()
	}
	ce := f.Close()
	if err != nil {
		return err
	}
	if ce != nil {
		return ce
	}
	if err = os.Rename(f.Name(), m.config); err != nil {
		return err
	}
	pid, err := os.ReadFile("/run/ganesha/ganesha.pid")
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(pid)))
	if err != nil || n <= 1 {
		return fmt.Errorf("invalid Ganesha PID")
	}
	comm, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", n))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(comm)) != "ganesha.nfsd" {
		return fmt.Errorf("unexpected Ganesha PID owner")
	}
	err = unix.Kill(n, unix.SIGHUP)
	if errors.Is(err, unix.ESRCH) {
		return nil
	}
	return err
}

func (m *manager) close() error {
	var result error
	for id, v := range m.volumes {
		if !v.mounted {
			continue
		}
		if e := unix.Unmount(filepath.Join(m.mounts, id), 0); e != nil && !errors.Is(e, unix.EINVAL) {
			result = errors.Join(result, e)
		}
	}
	return errors.Join(result, m.root.Close())
}

// Run serves the private Unix control socket in the NFS container.
func Run(ctx context.Context, root string) error {
	m, err := newManager(root, "/mnt/cloud-volumes", "/run/cloud-storage.conf")
	if err != nil {
		return err
	}
	defer func() { _ = m.close() }()
	socket := filepath.Join(root, "control.sock")
	if st, e := os.Lstat(socket); e == nil {
		if st.Mode()&os.ModeSocket == 0 {
			return fmt.Errorf("control path is not a socket")
		}
		if e = os.Remove(socket); e != nil {
			return e
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		return e
	}
	ln, err := (&net.ListenConfig{}).Listen(ctx, "unix", socket)
	if err != nil {
		return err
	}
	defer func() { _ = ln.Close() }()
	// #nosec G302 -- socket is shared only between trusted cloud/NFS containers.
	if err = os.Chmod(socket, 0o666); err != nil {
		return err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /volumes/{id}", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Bytes int64 `json:"bytes"`
		}
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil {
			http.Error(w, "invalid volume request", http.StatusBadRequest)
			return
		}
		if err := m.Ensure(r.Context(), r.PathValue("id"), req.Bytes); err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("DELETE /volumes/{id}", func(w http.ResponseWriter, r *http.Request) {
		if err := m.Delete(r.Context(), r.PathValue("id")); err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: time.Minute}
	go func() { <-ctx.Done(); _ = server.Close() }()
	err = server.Serve(ln)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func prepareStorageDirs(backing, mounts string) error {
	for _, path := range []string{backing, mounts, filepath.Join(backing, "images"), filepath.Join(backing, "volumes")} {
		// #nosec G301 G703 -- NFS workload UIDs need to traverse mount directories.
		if err := os.MkdirAll(path, 0o755); err != nil {
			return err
		}
		// #nosec G703 -- mount paths contain only internally validated volume identifiers.
		st, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if !st.IsDir() {
			return fmt.Errorf("storage directory is not a directory: %s", path)
		}
	}
	return nil
}

func unmountReleased(ctx context.Context, path string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		err := unix.Unmount(path, 0)
		if err == nil || errors.Is(err, unix.EINVAL) || errors.Is(err, unix.ENOENT) {
			return nil
		}
		if !errors.Is(err, unix.EBUSY) {
			return err
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("volume export is still in use: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}
