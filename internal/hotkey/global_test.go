package hotkey

import "testing"

func TestParseBinding(t *testing.T) {
	binding, ok := ParseBinding("Ctrl+Shift+C")
	if !ok {
		t.Fatal("expected preset binding")
	}
	if binding.Name != "Ctrl+Shift+C" {
		t.Fatalf("unexpected binding: %#v", binding)
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
