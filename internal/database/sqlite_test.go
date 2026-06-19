package database

import (
	"testing"

	"remotebash/internal/models"
)

func TestMigrate(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	// Verify tables exist.
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM clients").Scan(&count); err != nil {
		t.Fatalf("clients table: %v", err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM audit_log").Scan(&count); err != nil {
		t.Fatalf("audit_log table: %v", err)
	}
}

func TestInsertAndLoadClients(t *testing.T) {
	db, _ := Open(":memory:")
	defer db.Close()

	if err := InsertClient(db, &models.Client{Name: "test1", Host: "10.0.0.1", Port: 22, User: "root", Enabled: true}); err != nil {
		t.Fatalf("InsertClient: %v", err)
	}
	if err := InsertClient(db, &models.Client{Name: "test2", Host: "10.0.0.2", Port: 2222, User: "admin", Enabled: false, SafeRm: true}); err != nil {
		t.Fatalf("InsertClient: %v", err)
	}

	clients, err := LoadClients(db)
	if err != nil {
		t.Fatalf("LoadClients: %v", err)
	}
	if len(clients) != 2 {
		t.Fatalf("expected 2 clients, got %d", len(clients))
	}
	if clients[0].Name != "test1" {
		t.Errorf("expected test1, got %s", clients[0].Name)
	}
	if clients[1].SafeRm != true {
		t.Errorf("expected safe_rm=true for test2")
	}
}

func TestUpdateClient(t *testing.T) {
	db, _ := Open(":memory:")
	defer db.Close()

	InsertClient(db, &models.Client{Name: "test1", Host: "10.0.0.1", Port: 22, User: "root", Enabled: true})

	if err := UpdateClient(db, "test1", map[string]any{"host": "10.0.0.99", "port": 2222}); err != nil {
		t.Fatalf("UpdateClient: %v", err)
	}

	clients, _ := LoadClients(db)
	if clients[0].Host != "10.0.0.99" {
		t.Errorf("expected host 10.0.0.99, got %s", clients[0].Host)
	}
	if clients[0].Port != 2222 {
		t.Errorf("expected port 2222, got %d", clients[0].Port)
	}
}

func TestDeleteClient(t *testing.T) {
	db, _ := Open(":memory:")
	defer db.Close()

	InsertClient(db, &models.Client{Name: "test1", Host: "10.0.0.1", Port: 22, User: "root", Enabled: true})

	if err := DeleteClient(db, "test1"); err != nil {
		t.Fatalf("DeleteClient: %v", err)
	}

	clients, _ := LoadClients(db)
	if len(clients) != 0 {
		t.Errorf("expected 0 clients after delete, got %d", len(clients))
	}
}

func TestAuditCRUD(t *testing.T) {
	db, _ := Open(":memory:")
	defer db.Close()

	// Insert test client first (foreign key).
	InsertClient(db, &models.Client{Name: "audit_test", Host: "10.0.0.1", Port: 22, User: "root", Enabled: true})

	if err := InsertAudit(db, "audit_test", "ls -la", "total 0\n", 0, "/home", 125, true); err != nil {
		t.Fatalf("InsertAudit: %v", err)
	}
	if err := InsertAudit(db, "audit_test", "rm -rf /tmp/*", "error", 1, "/tmp", 45, false); err != nil {
		t.Fatalf("InsertAudit: %v", err)
	}

	// Query all.
	entries, err := QueryAudit(db, nil, nil, nil, 100, 0)
	if err != nil {
		t.Fatalf("QueryAudit: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}

	// Query with client filter.
	cn := "audit_test"
	entries, _ = QueryAudit(db, &cn, nil, nil, 100, 0)
	if len(entries) != 2 {
		t.Errorf("expected 2 entries for audit_test, got %d", len(entries))
	}

	// Count.
	count, _ := CountAudit(db, &cn, nil, nil)
	if count != 2 {
		t.Errorf("expected count 2, got %d", count)
	}

	// Delete single.
	n, _ := DeleteAuditByID(db, entries[0].ID)
	if n != 1 {
		t.Errorf("expected 1 deleted, got %d", n)
	}
	entries, _ = QueryAudit(db, nil, nil, nil, 100, 0)
	if len(entries) != 1 {
		t.Errorf("expected 1 entry after delete, got %d", len(entries))
	}
}

func TestIsoToSQLite(t *testing.T) {
	cases := []struct{ in, want string }{
		{"2026-06-12T11:30:00.123Z", "2026-06-12 11:30:00"},
		{"2026-06-12T11:30:00", "2026-06-12 11:30:00"},
		{"2026-06-12 11:30:00", "2026-06-12 11:30:00"},
		{"", ""},
	}
	for _, tc := range cases {
		got := isoToSQLite(tc.in)
		if got != tc.want {
			t.Errorf("isoToSQLite(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
