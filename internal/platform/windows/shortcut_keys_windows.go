//go:build windows

package windows

const (
	vkBackspace = 0x08
	vkControl   = 0x11
	vkMenu      = 0x12
	vkPause     = 0x13
	vkShift     = 0x10
	vkSnapshot  = 0x2C
	vkInsert    = 0x2D
)

var shortcutGetAsyncKeyState = windowUser32.NewProc("GetAsyncKeyState")

func PressedSpecialShortcutKeys() []string {
	keys := make([]string, 0, 6)
	for _, item := range []struct {
		vk   int
		name string
	}{
		{vk: vkControl, name: "Ctrl"},
		{vk: vkMenu, name: "Alt"},
		{vk: vkShift, name: "Shift"},
		{vk: vkSnapshot, name: "PrintScreen"},
		{vk: vkInsert, name: "Insert"},
		{vk: vkPause, name: "Pause"},
		{vk: vkBackspace, name: "Backspace"},
	} {
		if asyncKeyDown(item.vk) {
			keys = append(keys, item.name)
		}
	}
	return keys
}

func asyncKeyDown(vk int) bool {
	state, _, _ := shortcutGetAsyncKeyState.Call(uintptr(vk))
	return state&0x8000 != 0 || state&1 != 0
}
