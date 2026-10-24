//go:build unix

package pathx

import (
	"os"
	"syscall"
)

// SameDevice reports whether a and b live on the same filesystem, which is
// what makes os.Rename between them atomic.
func SameDevice(a, b string) (bool, error) {
	sa, err := os.Stat(a)
	if err != nil {
		return false, err
	}
	sb, err := os.Stat(b)
	if err != nil {
		return false, err
	}
	da, okA := sa.Sys().(*syscall.Stat_t)
	db, okB := sb.Sys().(*syscall.Stat_t)
	if !okA || !okB {
		return true, nil
	}
	return da.Dev == db.Dev, nil
}
