package storage

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

func TestFormatImageCreatesMountableExt4(t *testing.T) {
	f, err := os.Create(filepath.Join(t.TempDir(), "disk.img"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if err := formatImage(f, 64<<20); err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 1024)
	if _, err := f.ReadAt(b, 1024); err != nil {
		t.Fatal(err)
	}
	if binary.LittleEndian.Uint16(b[56:58]) != 0xef53 {
		t.Fatal("missing ext4 magic")
	}
	if got := binary.LittleEndian.Uint32(b[4:8]); got != 16384 {
		t.Fatalf("block count=%d", got)
	}
	if got := binary.LittleEndian.Uint32(b[100:104]); got&0x2100 != 0 {
		t.Fatalf("unsupported kernel quota features enabled: %x", got)
	}
}

func TestFormatImageRejectsInvalidCapacity(t *testing.T) {
	for _, n := range []int64{0, -1, 1, 1 << 20, (1 << 40) + 1} {
		f, err := os.Create(filepath.Join(t.TempDir(), "disk.img"))
		if err != nil {
			t.Fatal(err)
		}
		if err := formatImage(f, n); err == nil {
			t.Fatalf("accepted capacity %d", n)
		}
		_ = f.Close()
	}
}
