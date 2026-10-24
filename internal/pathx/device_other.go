//go:build !unix

package pathx

// SameDevice is not implemented on this platform yet; os.Rename will report
// cross-device errors itself.
func SameDevice(a, b string) (bool, error) {
	return true, nil
}
