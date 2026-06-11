//go:build !windows

package windows

func PressedSpecialShortcutKeys() []string {
	return nil
}
