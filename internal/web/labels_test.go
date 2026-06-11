package web

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
)

// ctxWithQuery builds an echo.Context whose form values come from the query
// string (c.FormValue reads both query and body).
func ctxWithQuery(query string) echo.Context {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/?"+query, nil)
	return e.NewContext(req, httptest.NewRecorder())
}

func TestResolveLabelSize(t *testing.T) {
	const defW, defH, defGap = 50, 25, 2

	cases := []struct {
		name      string
		query     string
		w, h, gap int
	}{
		{"default keeps settings", "label_size=default", 50, 25, 2},
		{"empty keeps settings", "", 50, 25, 2},
		{"preset 40x30", "label_size=40x30", 40, 30, 2},
		{"preset 100x50", "label_size=100x50", 100, 50, 2},
		{"custom overrides", "label_size=custom&label_w=70&label_h=40&label_gap=3", 70, 40, 3},
		{"custom partial falls back", "label_size=custom&label_w=70", 70, 25, 2},
		{"bad preset falls back", "label_size=banana", 50, 25, 2},
		{"clamps tiny to min", "label_size=5x5", 10, 10, 2},
		{"clamps huge to max", "label_size=900x900", 200, 200, 2},
	}
	for _, tc := range cases {
		w, h, gap := resolveLabelSize(ctxWithQuery(tc.query), defW, defH, defGap)
		if w != tc.w || h != tc.h || gap != tc.gap {
			t.Errorf("%s: got %dx%d gap %d, want %dx%d gap %d", tc.name, w, h, gap, tc.w, tc.h, tc.gap)
		}
	}
}
