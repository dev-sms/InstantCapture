package tray

import "testing"

func TestEventShowValue(t *testing.T) {
	if EventShow == 0 {
		t.Fatal("EventShow should be non-zero")
	}
}
