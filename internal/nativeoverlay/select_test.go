package nativeoverlay

import (
	"errors"
	"testing"
)

func TestErrUnavailableSupportsErrorsIs(t *testing.T) {
	if !errors.Is(ErrUnavailable, ErrUnavailable) {
		t.Fatal("ErrUnavailable should match itself")
	}
}
