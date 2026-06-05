//go:build !windows

package windows

func EnableDPIAwareness() error {
	return nil
}
