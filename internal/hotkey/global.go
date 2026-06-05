package hotkey

import designhotkey "golang.design/x/hotkey"

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
	return Binding{}, false
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
