//go:build darwin || linux

package postproc

import (
	"os"
	"syscall"
)

func availableBytesOS(path string) (uint64, error) {
	info := syscall.Statfs_t{}
	if err := syscall.Statfs(path, &info); err != nil {
		return 0, &os.PathError{Op: "statfs", Path: path, Err: err}
	}
	return uint64(info.Bavail) * uint64(info.Bsize), nil
}
