package minikube

import (
	"context"
	_ "embed"
	"errors"
	"fmt"

	"github.com/miciav/minikube-cloud-lab/internal/infra"
)

//go:embed instance-disk.sh
var diskScript string

//go:embed instance-disk.service
var diskUnit string

// SetInstanceDisk enables or removes the guest service holding the loop device.
func (i *Infra) SetInstanceDisk(ctx context.Context, instance, name string, attach bool) error {
	script := `set -eu
exec 8>/run/mcl-disk-control.lock
flock -x 8
unit=mcl-disk@"$DISK".service
image=/var/lib/mcl-disk-images/$DISK
test -f "$image"
if [ "$ATTACH" = true ]; then
 mkdir -p /usr/local/libexec
 printf '%s' "$SCRIPT" > /usr/local/libexec/mcl-instance-disk
 chmod 755 /usr/local/libexec/mcl-instance-disk
 printf '%s' "$UNIT" > /etc/systemd/system/mcl-disk@.service
 systemctl daemon-reload
 systemctl enable --now "$unit"
 test -b "/dev/cloud-disks/$DISK"
else
 device=$(losetup -j "$image" --noheadings --output NAME)
 if [ -n "$device" ] && lsblk -nr -o MOUNTPOINTS "$device" | grep -q '[^[:space:]]'; then
  exit 32
 fi
 if systemctl cat "$unit" >/dev/null 2>&1; then systemctl disable --now "$unit"; fi
 if [ -n "$(losetup -j "$image")" ]; then
  systemctl enable --now "$unit"
  exit 33
 fi
fi`
	client, err := dockerClient()
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()
	env := []string{"DISK=" + name, fmt.Sprintf("ATTACH=%t", attach), "SCRIPT=" + diskScript, "UNIT=" + diskUnit}
	err = execRoot(ctx, client, instancePrefix+instance, env, script)
	switch {
	case errors.Is(err, guestExitCode(32)):
		return fmt.Errorf("%w: disk is mounted; unmount it before detaching", infra.ErrDiskBusy)
	case errors.Is(err, guestExitCode(33)):
		return fmt.Errorf("%w: close all users before detaching", infra.ErrDiskBusy)
	default:
		return err
	}
}
