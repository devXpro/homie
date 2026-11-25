package hub

import (
	"errors"
	"testing"
)

func TestErrorsAreDistinct(t *testing.T) {
	errs := []error{ErrUnknownTool, ErrPluginDown}
	for i, a := range errs {
		for j, b := range errs {
			if (i == j) != errors.Is(a, b) {
				t.Errorf("errors.Is(%v, %v) = %v", a, b, errors.Is(a, b))
			}
		}
	}
}

func TestAudienceValues(t *testing.T) {
	if AudienceAssistant != "assistant" || AudienceAdmin != "admin" {
		t.Fatal("audience values changed")
	}
}
