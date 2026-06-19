package api_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"remotebash/internal/api"
	"remotebash/internal/database"
	"remotebash/internal/manager"
)

func setupTestMux(t *testing.T) *http.ServeMux {
	t.Helper()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	mgr := manager.New(db)
	if err := mgr.Load(); err != nil {
		t.Fatalf("load: %v", err)
	}

	mux := http.NewServeMux()
	routes := &api.Routes{Mgr: mgr}
	routes.Register(mux)
	return mux
}

func TestListClients_Empty(t *testing.T) {
	mux := setupTestMux(t)

	req := httptest.NewRequest("GET", "/api/clients", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var clients []map[string]any
	json.NewDecoder(rec.Body).Decode(&clients)
	if len(clients) != 0 {
		t.Errorf("expected 0 clients, got %d", len(clients))
	}
}

func TestAddClient_Success(t *testing.T) {
	mux := setupTestMux(t)

	// Disable auto_connect to avoid real SSH connection attempt.
	body := `{"name":"test","host":"10.0.0.1","user":"root","password":"secret","auto_connect":false}`
	req := httptest.NewRequest("POST", "/api/clients", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	// List should now have 1 client.
	req2 := httptest.NewRequest("GET", "/api/clients", nil)
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, req2)

	var clients []map[string]any
	json.NewDecoder(rec2.Body).Decode(&clients)
	if len(clients) != 1 {
		t.Errorf("expected 1 client, got %d", len(clients))
	}
	if clients[0]["name"] != "test" {
		t.Errorf("expected name 'test', got %v", clients[0]["name"])
	}
}

func TestAddClient_Validation(t *testing.T) {
	mux := setupTestMux(t)

	body := `{"name":"","host":"","user":""}`
	req := httptest.NewRequest("POST", "/api/clients", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestAddClient_InvalidName(t *testing.T) {
	mux := setupTestMux(t)

	body := `{"name":"my host!","host":"10.0.0.1","user":"root","password":"x"}`
	req := httptest.NewRequest("POST", "/api/clients", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid name, got %d", rec.Code)
	}
}

func TestRemoveClient_NotFound(t *testing.T) {
	mux := setupTestMux(t)

	req := httptest.NewRequest("DELETE", "/api/clients/nonexistent", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec.Code)
	}
}

func TestDisconnectClient_NotFound(t *testing.T) {
	mux := setupTestMux(t)

	req := httptest.NewRequest("POST", "/api/clients/nonexistent/disconnect", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec.Code)
	}
}

func TestAuditList_Empty(t *testing.T) {
	mux := setupTestMux(t)

	req := httptest.NewRequest("GET", "/api/audit", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var resp struct {
		Entries []any `json:"entries"`
		Total   int   `json:"total"`
	}
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp.Total != 0 {
		t.Errorf("expected 0 total, got %d", resp.Total)
	}
}

func TestStaticAssets_Served(t *testing.T) {
	// Verify static files can be accessed when served through embed.
	// This is tested indirectly since embed requires the binary.
	t.Skip("requires compiled binary with embedded assets")
}

// Ensure unused imports are used.
var _ = io.EOF
