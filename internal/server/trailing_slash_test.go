package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTrailingSlashRedirect(t *testing.T) {
	// Inner handler that records when it's called directly.
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})
	handler := trailingSlashRedirect(inner)

	tests := []struct {
		name           string
		method         string
		path           string
		query          string
		wantStatus     int
		wantLocation   string
		wantBody       string
	}{
		{
			name:       "no trailing slash — passthrough",
			method:     "GET",
			path:       "/mcp",
			wantStatus: http.StatusOK,
			wantBody:   "ok",
		},
		{
			name:         "trailing slash — redirect",
			method:       "GET",
			path:         "/mcp/",
			wantStatus:   http.StatusMovedPermanently,
			wantLocation: "/mcp",
		},
		{
			name:         "trailing slash with query — redirect preserving query",
			method:       "POST",
			path:         "/mcp/",
			query:        "foo=bar",
			wantStatus:   http.StatusMovedPermanently,
			wantLocation: "/mcp?foo=bar",
		},
		{
			name:       "root — passthrough",
			method:     "GET",
			path:       "/",
			wantStatus: http.StatusOK,
			wantBody:   "ok",
		},
		{
			name:         "API path with trailing slash — redirect",
			method:       "GET",
			path:         "/api/clients/",
			wantStatus:   http.StatusMovedPermanently,
			wantLocation: "/api/clients",
		},
		{
			name:       "static prefix — passthrough",
			method:     "GET",
			path:       "/static/js/app.js",
			wantStatus: http.StatusOK,
			wantBody:   "ok",
		},
		{
			name:       "static root — passthrough (prefix pattern)",
			method:     "GET",
			path:       "/static/",
			wantStatus: http.StatusOK,
			wantBody:   "ok",
		},
		{
			name:       "health — passthrough (no slash)",
			method:     "GET",
			path:       "/health",
			wantStatus: http.StatusOK,
			wantBody:   "ok",
		},
		{
			name:       "audit — passthrough (no slash)",
			method:     "GET",
			path:       "/audit",
			wantStatus: http.StatusOK,
			wantBody:   "ok",
		},
		{
			name:         "audit with trailing slash — redirect",
			method:       "GET",
			path:         "/audit/",
			wantStatus:   http.StatusMovedPermanently,
			wantLocation: "/audit",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			target := tt.path
			if tt.query != "" {
				target += "?" + tt.query
			}
			req := httptest.NewRequest(tt.method, target, nil)
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantStatus)
			}

			if tt.wantLocation != "" {
				loc := rec.Header().Get("Location")
				if loc != tt.wantLocation {
					t.Errorf("Location = %q, want %q", loc, tt.wantLocation)
				}
			}

			if tt.wantBody != "" {
				if body := rec.Body.String(); body != tt.wantBody {
					t.Errorf("body = %q, want %q", body, tt.wantBody)
				}
			}
		})
	}
}
