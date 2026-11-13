package content

import (
	"testing"

	"code-sprout-cms/internal/platform/apperr"
)

// A page's locale is part of its URL (/{locale}/… for anything but English), so only the
// languages the site serves may have pages.
func TestCreateLocaleMustBeAContentLanguage(t *testing.T) {
	for _, ok := range []string{"en", "hi"} {
		if err := validateCreate(CreateInput{Locale: ok}); err != nil {
			t.Errorf("%q refused: %v", ok, err)
		}
	}
	for _, bad := range []string{"fr", "pt-BR", "english", ""} {
		err := validateCreate(CreateInput{Locale: bad})
		if e, ok := apperr.As(err); !ok || e.Kind != apperr.KindInvalid || e.Fields["locale"] == "" {
			t.Errorf("%q accepted or wrong error: %v", bad, err)
		}
	}
}
