// Package nfs manages bounded NFS backing filesystems.
package nfs

import (
	"fmt"
	"os"

	"github.com/diskfs/go-diskfs/backend/file"
	"github.com/diskfs/go-diskfs/filesystem/ext4"
)

func formatImage(f *os.File, size int64) error {
	if size < 16<<20 || size > 1<<40 || size%(1<<20) != 0 {
		return fmt.Errorf("volume capacity must be a whole MiB between 16Mi and 1Ti")
	}
	if err := f.Truncate(size); err != nil {
		return err
	}
	_, err := ext4.Create(file.New(f, false), size, 0, 512, &ext4.Params{SectorsPerBlock: 8, Features: []ext4.FeatureOpt{
		ext4.WithFeatureReservedGDTBlocksForExpansion(false),
		ext4.WithFeatureQuota(false), ext4.WithFeatureProjectQuotas(false),
	}})
	if err != nil {
		return err
	}
	return f.Sync()
}
