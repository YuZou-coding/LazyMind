//go:build !windows

package server

import (
	"fmt"
	"os"
	"runtime"
	"syscall"
)

func workspaceDirectoryIdentity(_ string, info os.FileInfo) (string, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", fmt.Errorf("directory identity unavailable")
	}
	return fmt.Sprintf("fsid:%s:%d:%d", runtime.GOOS, stat.Dev, stat.Ino), nil
}
