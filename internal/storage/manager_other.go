//go:build !linux

package storage

import (
	"context"
	"fmt"
)

// Run is available only inside the Linux NFS container.
func Run(context.Context, string) error {
	return fmt.Errorf("storage filesystems require the Linux NFS container")
}
