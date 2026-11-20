package httpapi

import (
	"net/http/httptest"
	"testing"

	"kn.local/api/internal/service"
)

func TestReportLang(t *testing.T) {
	for _, tc := range []struct {
		query, header string
		want          service.Lang
	}{
		{"", "", service.LangEN},
		{"", "ru-RU,ru;q=0.9,en;q=0.8", service.LangRU},
		{"", "en-US,en;q=0.9,ru;q=0.8", service.LangEN},
		{"", "RU", service.LangRU},
		{"", "de-DE", service.LangEN},
		{"?lang=ru", "en-US", service.LangRU},
		{"?lang=en", "ru-RU", service.LangEN},
	} {
		r := httptest.NewRequest("GET", "/v1/health"+tc.query, nil)
		if tc.header != "" {
			r.Header.Set("Accept-Language", tc.header)
		}
		if got := reportLang(r); got != tc.want {
			t.Errorf("lang %q, Accept-Language %q: got %s, want %s", tc.query, tc.header, got, tc.want)
		}
	}
}
