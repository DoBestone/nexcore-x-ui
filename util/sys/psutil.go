package sys

import (
	"os"
	"path/filepath"
)

// HostProc returns the path to /proc, honoring the HOST_PROC env var that
// gopsutil also respects. Replaces the previous //go:linkname hack into
// gopsutil's internal package, which broke under v4's new module layout.
func HostProc(combineWith ...string) string {
	root := os.Getenv("HOST_PROC")
	if root == "" {
		root = "/proc"
	}
	if len(combineWith) == 0 {
		return root
	}
	parts := append([]string{root}, combineWith...)
	return filepath.Join(parts...)
}
