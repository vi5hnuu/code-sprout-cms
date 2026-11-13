package locales

import "testing"

func TestContentLocales(t *testing.T) {
	for _, code := range []string{"en", "hi"} {
		if !IsContent(code) {
			t.Errorf("%q should be a content locale", code)
		}
	}
	for _, code := range []string{"", "fr", "EN", "go", "hi-IN"} {
		if IsContent(code) {
			t.Errorf("%q should not be a content locale", code)
		}
	}
	if !IsContent(Default) {
		t.Errorf("the default locale %q must be a content locale", Default)
	}
}
