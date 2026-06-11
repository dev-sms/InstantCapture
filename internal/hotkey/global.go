package hotkey

import (
	"fmt"
	"strings"

	designhotkey "golang.design/x/hotkey"
)

type Binding struct {
	Name      string
	Modifiers []designhotkey.Modifier
	Key       designhotkey.Key
}

var presetBindings = []Binding{
	{Name: "Ctrl+Shift+C", Modifiers: []designhotkey.Modifier{designhotkey.ModCtrl, designhotkey.ModShift}, Key: designhotkey.KeyC},
	{Name: "Ctrl+Alt+C", Modifiers: []designhotkey.Modifier{designhotkey.ModCtrl, designhotkey.ModAlt}, Key: designhotkey.KeyC},
	{Name: "Ctrl+Shift+X", Modifiers: []designhotkey.Modifier{designhotkey.ModCtrl, designhotkey.ModShift}, Key: designhotkey.KeyX},
	{Name: "PrintScreen", Modifiers: nil, Key: designhotkey.Key(0x2C)},
}

type GlobalRegistrar struct {
	registrations []*registration
}

type registration struct {
	hotkey *designhotkey.Hotkey
	stop   chan struct{}
}

func NewGlobalRegistrar() *GlobalRegistrar {
	return &GlobalRegistrar{}
}

func (r *GlobalRegistrar) RegisterCapture(callback func()) error {
	return r.RegisterCaptureBinding(DefaultBinding(), callback)
}

func (r *GlobalRegistrar) RegisterCaptureBinding(binding Binding, callback func()) error {
	registered, err := newRegistration(binding, callback)
	if err != nil {
		return err
	}
	r.registrations = append(r.registrations, registered)
	return nil
}

func (r *GlobalRegistrar) RebindCapture(binding Binding, callback func()) error {
	old := append([]*registration(nil), r.registrations...)
	registered, err := newRegistration(binding, callback)
	if err != nil {
		return err
	}
	for _, item := range old {
		item.close()
	}
	r.registrations = []*registration{registered}
	return nil
}

func (r *GlobalRegistrar) Close() {
	r.unregisterAll()
}

func (r *GlobalRegistrar) unregisterAll() {
	for _, item := range r.registrations {
		item.close()
	}
	r.registrations = nil
}

func newRegistration(binding Binding, callback func()) (*registration, error) {
	hk := designhotkey.New(binding.Modifiers, binding.Key)
	if err := hk.Register(); err != nil {
		return nil, err
	}
	registered := &registration{hotkey: hk, stop: make(chan struct{})}
	go registered.listen(callback)
	return registered, nil
}

func (r *registration) listen(callback func()) {
	for {
		select {
		case _, ok := <-r.hotkey.Keydown():
			if !ok {
				return
			}
			callback()
		case <-r.stop:
			return
		}
	}
}

func (r *registration) close() {
	select {
	case <-r.stop:
	default:
		close(r.stop)
	}
	_ = r.hotkey.Unregister()
}

func DefaultBinding() Binding {
	return presetBindings[0]
}

func Presets() []Binding {
	return append([]Binding(nil), presetBindings...)
}

func ParseBinding(name string) (Binding, bool) {
	for _, binding := range presetBindings {
		if binding.Name == name {
			return binding, true
		}
	}
	parts := strings.Split(name, "+")
	if len(parts) == 0 {
		return Binding{}, false
	}
	modifiers := make([]string, 0, len(parts)-1)
	var keyName string
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			return Binding{}, false
		}
		if _, _, ok := modifierFromName(part); ok {
			if keyName != "" {
				return Binding{}, false
			}
			modifiers = append(modifiers, part)
			continue
		}
		if keyName != "" {
			return Binding{}, false
		}
		keyName = part
	}
	return BindingFromNames(modifiers, keyName)
}

func BindingFromNames(modifierNames []string, keyName string) (Binding, bool) {
	mods, modLabels, ok := normalizeModifiers(modifierNames)
	if !ok {
		return Binding{}, false
	}
	keyCode, keyLabel, ok := keyFromName(keyName)
	if !ok {
		return Binding{}, false
	}
	if len(mods) == 0 && keyLabel != "PrintScreen" {
		return Binding{}, false
	}
	if len(mods)+1 > 3 {
		return Binding{}, false
	}
	nameParts := append([]string{}, modLabels...)
	nameParts = append(nameParts, keyLabel)
	return Binding{Name: strings.Join(nameParts, "+"), Modifiers: mods, Key: keyCode}, true
}

func normalizeModifiers(names []string) ([]designhotkey.Modifier, []string, bool) {
	type modifierItem struct {
		label string
		mod   designhotkey.Modifier
		seen  bool
	}
	items := []modifierItem{
		{label: "Ctrl", mod: designhotkey.ModCtrl},
		{label: "Alt", mod: designhotkey.ModAlt},
		{label: "Shift", mod: designhotkey.ModShift},
	}
	for _, name := range names {
		_, mod, ok := modifierFromName(name)
		if !ok {
			return nil, nil, false
		}
		matched := false
		for i := range items {
			if items[i].mod == mod {
				if items[i].seen {
					return nil, nil, false
				}
				items[i].seen = true
				matched = true
				break
			}
		}
		if !matched {
			return nil, nil, false
		}
	}
	mods := make([]designhotkey.Modifier, 0, len(names))
	labels := make([]string, 0, len(names))
	for _, item := range items {
		if item.seen {
			mods = append(mods, item.mod)
			labels = append(labels, item.label)
		}
	}
	return mods, labels, true
}

func modifierFromName(name string) (string, designhotkey.Modifier, bool) {
	switch strings.ToUpper(strings.TrimSpace(name)) {
	case "CTRL", "CONTROL":
		return "Ctrl", designhotkey.ModCtrl, true
	case "ALT", "OPTION":
		return "Alt", designhotkey.ModAlt, true
	case "SHIFT":
		return "Shift", designhotkey.ModShift, true
	default:
		return "", 0, false
	}
}

func keyFromName(name string) (designhotkey.Key, string, bool) {
	upper := strings.ToUpper(strings.TrimSpace(name))
	if len(upper) == 1 {
		ch := upper[0]
		if ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' {
			return designhotkey.Key(ch), string(ch), true
		}
	}
	if strings.HasPrefix(upper, "F") && len(upper) > 1 {
		var number int
		if _, err := fmt.Sscanf(upper, "F%d", &number); err == nil && number >= 1 && number <= 20 {
			return designhotkey.Key(0x6F + number), fmt.Sprintf("F%d", number), true
		}
	}
	switch upper {
	case "PRINTSCREEN", "PRINT SCREEN", "PRTSC", "PRTSCN", "SNAPSHOT":
		return designhotkey.Key(0x2C), "PrintScreen", true
	case "INSERT", "INS":
		return designhotkey.Key(0x2D), "Insert", true
	case "PAUSE", "BREAK":
		return designhotkey.Key(0x13), "Pause", true
	case "BACKSPACE", "BACK":
		return designhotkey.Key(0x08), "Backspace", true
	case "SPACE", "SPACEBAR":
		return designhotkey.KeySpace, "Space", true
	case "ENTER", "RETURN":
		return designhotkey.KeyReturn, "Enter", true
	case "ESC", "ESCAPE":
		return designhotkey.KeyEscape, "Escape", true
	case "DELETE", "DEL":
		return designhotkey.KeyDelete, "Delete", true
	case "TAB":
		return designhotkey.KeyTab, "Tab", true
	case "HOME":
		return designhotkey.Key(0x24), "Home", true
	case "END":
		return designhotkey.Key(0x23), "End", true
	case "PAGEUP", "PAGE UP", "PGUP":
		return designhotkey.Key(0x21), "PageUp", true
	case "PAGEDOWN", "PAGE DOWN", "PGDN":
		return designhotkey.Key(0x22), "PageDown", true
	case "LEFT", "LEFTARROW":
		return designhotkey.KeyLeft, "Left", true
	case "RIGHT", "RIGHTARROW":
		return designhotkey.KeyRight, "Right", true
	case "UP", "UPARROW":
		return designhotkey.KeyUp, "Up", true
	case "DOWN", "DOWNARROW":
		return designhotkey.KeyDown, "Down", true
	default:
		return 0, "", false
	}
}

func NextPreset(current string) Binding {
	presets := Presets()
	for i, binding := range presets {
		if binding.Name == current {
			return presets[(i+1)%len(presets)]
		}
	}
	return DefaultBinding()
}
