package hotkey

import (
	"testing"

	designhotkey "golang.design/x/hotkey"
)

func TestParseBinding(t *testing.T) {
	binding, ok := ParseBinding("Ctrl+Shift+C")
	if !ok {
		t.Fatal("expected preset binding")
	}
	if binding.Name != "Ctrl+Shift+C" {
		t.Fatalf("unexpected binding: %#v", binding)
	}
}

func TestParseBindingCustomCombination(t *testing.T) {
	binding, ok := ParseBinding("ctrl + alt + k")
	if !ok {
		t.Fatal("expected custom binding")
	}
	if binding.Name != "Ctrl+Alt+K" {
		t.Fatalf("unexpected normalized name: %q", binding.Name)
	}
	if binding.Key != designhotkey.KeyK {
		t.Fatalf("unexpected key: %#v", binding.Key)
	}
	wantMods := []designhotkey.Modifier{designhotkey.ModCtrl, designhotkey.ModAlt}
	if len(binding.Modifiers) != len(wantMods) {
		t.Fatalf("unexpected modifiers: %#v", binding.Modifiers)
	}
	for i := range wantMods {
		if binding.Modifiers[i] != wantMods[i] {
			t.Fatalf("modifier %d = %#v, want %#v", i, binding.Modifiers[i], wantMods[i])
		}
	}
}

func TestBindingFromNamesRejectsBareLetters(t *testing.T) {
	if _, ok := BindingFromNames(nil, "K"); ok {
		t.Fatal("bare letter should not be accepted as a capture hotkey")
	}
}

func TestParseBindingRejectsMalformedFunctionKey(t *testing.T) {
	if _, ok := ParseBinding("Ctrl+F1garbage"); ok {
		t.Fatal("malformed function key should be rejected")
	}
}

func TestBindingFromNamesSpecialKeys(t *testing.T) {
	printScreen, ok := BindingFromNames(nil, "PrintScreen")
	if !ok {
		t.Fatal("PrintScreen should be accepted without modifiers")
	}
	if printScreen.Name != "PrintScreen" || printScreen.Key != designhotkey.Key(0x2C) {
		t.Fatalf("unexpected PrintScreen binding: %#v", printScreen)
	}

	for _, tc := range []struct {
		key  string
		name string
		code designhotkey.Key
	}{
		{key: "Home", name: "Ctrl+Home", code: designhotkey.Key(0x24)},
		{key: "Page Down", name: "Ctrl+PageDown", code: designhotkey.Key(0x22)},
		{key: "Backspace", name: "Ctrl+Backspace", code: designhotkey.Key(0x08)},
		{key: "Insert", name: "Ctrl+Insert", code: designhotkey.Key(0x2D)},
		{key: "Pause", name: "Ctrl+Pause", code: designhotkey.Key(0x13)},
	} {
		binding, ok := BindingFromNames([]string{"Ctrl"}, tc.key)
		if !ok {
			t.Fatalf("%s should be accepted", tc.key)
		}
		if binding.Name != tc.name || binding.Key != tc.code {
			t.Fatalf("%s binding = %#v, want name %q key %#v", tc.key, binding, tc.name, tc.code)
		}
	}
}

func TestNextPreset(t *testing.T) {
	next := NextPreset("Ctrl+Shift+C")
	if next.Name == "Ctrl+Shift+C" {
		t.Fatal("expected next preset to change")
	}
	if got := NextPreset("unknown"); got.Name != DefaultBinding().Name {
		t.Fatalf("unknown preset should return default, got %q", got.Name)
	}
}
