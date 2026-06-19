package manager_test

import (
	"testing"

	"remotebash/internal/database"
	"remotebash/internal/manager"
	"remotebash/internal/models"
)

func setupManager(t *testing.T) *manager.ConnectionManager {
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
	return mgr
}

func TestAddClient(t *testing.T) {
	mgr := setupManager(t)

	info, err := mgr.Add("server1", "10.0.0.1", "root", "pass123", "", 22, true, false)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if info.Name != "server1" {
		t.Errorf("expected name 'server1', got %v", info.Name)
	}
}

func TestAddDuplicate(t *testing.T) {
	mgr := setupManager(t)

	mgr.Add("server1", "10.0.0.1", "root", "pass", "", 22, true, false)
	_, err := mgr.Add("server1", "10.0.0.2", "root", "pass", "", 22, true, false)
	if err == nil {
		t.Error("expected error for duplicate client")
	}
}

func TestRemove(t *testing.T) {
	mgr := setupManager(t)

	mgr.Add("server1", "10.0.0.1", "root", "pass", "", 22, true, false)
	if err := mgr.Remove("server1"); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	_, err := mgr.Get("server1")
	if err == nil {
		t.Error("expected error for removed client")
	}
}

func TestGetNotFound(t *testing.T) {
	mgr := setupManager(t)

	_, err := mgr.Get("nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent client")
	}
}

func TestListAll(t *testing.T) {
	mgr := setupManager(t)

	mgr.Add("a", "10.0.0.1", "root", "pass", "", 22, true, false)
	mgr.Add("b", "10.0.0.2", "root", "pass", "", 22, false, true)

	all := mgr.ListAll()
	if len(all) != 2 {
		t.Errorf("expected 2 clients, got %d", len(all))
	}

	enabled := mgr.ListEnabled()
	if len(enabled) != 1 {
		t.Errorf("expected 1 enabled client, got %d", len(enabled))
	}
}

func TestRemoveBlockedByJumpHostDependency(t *testing.T) {
	mgr := setupManager(t)

	mgr.Add("jump", "10.0.0.1", "root", "pass", "", 22, true, false)
	mgr.Add("target", "192.168.1.1", "root", "pass", "jump", 22, true, false)

	err := mgr.Remove("jump")
	if err == nil {
		t.Error("expected error when removing jump host with dependent clients")
	}
}

func TestCycleDetection(t *testing.T) {
	mgr := setupManager(t)

	mgr.Add("a", "10.0.0.1", "root", "pass", "", 22, true, false)
	mgr.Add("b", "192.168.1.1", "root", "pass", "a", 22, true, false)
	mgr.Add("c", "192.168.1.2", "root", "pass", "b", 22, true, false)

	// Try to make a -> c (creates a -> c -> b -> a cycle).
	update := models.ClientUpdate{}
	via := "c"
	update.Via = &via
	_, err := mgr.Update("a", update)
	if err == nil {
		t.Error("expected error for 3-node cyclic jump host reference")
	}

	// Self-reference should also be caught.
	mgr.Add("self1", "10.0.0.1", "root", "pass", "", 22, true, false)
	via2 := "self1"
	update2 := models.ClientUpdate{}
	update2.Via = &via2
	_, err = mgr.Update("self1", update2)
	if err == nil {
		t.Error("expected error for self-referencing jump host")
	}
}

func TestAuditLog(t *testing.T) {
	mgr := setupManager(t)

	mgr.Add("test", "10.0.0.1", "root", "pass", "", 22, true, false)
	mgr.LogAudit("test", "ls", "file.txt", 0, "/home", 100, true)
	mgr.LogAudit("test", "rm bad", "error", 1, "/tmp", 50, false)

	cn := "test"
	resp, err := mgr.AuditList(&cn, nil, nil, 100, 0)
	if err != nil {
		t.Fatalf("AuditList: %v", err)
	}
	if resp.Total != 2 {
		t.Errorf("expected 2 audit entries, got %d", resp.Total)
	}
}

func TestUpdateClient(t *testing.T) {
	mgr := setupManager(t)

	mgr.Add("server1", "10.0.0.1", "root", "pass", "", 22, true, false)

	update := models.ClientUpdate{}
	newHost := "10.0.0.99"
	update.Host = &newHost
	newSafeRm := true
	update.SafeRm = &newSafeRm

	info, err := mgr.Update("server1", update)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if info.Host != "10.0.0.99" {
		t.Errorf("expected host 10.0.0.99, got %v", info.Host)
	}
	if info.SafeRm != true {
		t.Errorf("expected safe_rm=true, got %v", info.SafeRm)
	}
}
