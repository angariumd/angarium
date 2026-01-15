package controller

import (
	"os"
	"testing"
	"time"

	"github.com/angariumd/angarium/internal/auth"
	"github.com/angariumd/angarium/internal/db"
)

func TestNodeStaleness(t *testing.T) {
	dbPath := "test_staleness.db"
	defer os.Remove(dbPath)

	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	if err := database.Init(); err != nil {
		t.Fatal(err)
	}

	server := NewServer(database, auth.NewAuthenticator(database), "test-token")

	// Insert a node that is currently UP but staleness is imminent
	staleTime := time.Now().Add(-30 * time.Second)
	_, err = database.Exec(`
		INSERT INTO nodes (id, status, last_heartbeat_at, agent_version)
		VALUES (?, 'UP', ?, ?)
	`, "stale-node", staleTime, "v0.1.0")
	if err != nil {
		t.Fatal(err)
	}

	// Insert a fresh node
	_, err = database.Exec(`
		INSERT INTO nodes (id, status, last_heartbeat_at, agent_version)
		VALUES (?, 'UP', ?, ?)
	`, "fresh-node", time.Now(), "v0.1.0")
	if err != nil {
		t.Fatal(err)
	}

	// Run detection
	server.detectStaleNodes()

	// Verify stale node is DOWN
	var status string
	err = database.QueryRow("SELECT status FROM nodes WHERE id = ?", "stale-node").Scan(&status)
	if err != nil {
		t.Fatal(err)
	}
	if status != "DOWN" {
		t.Errorf("expected status DOWN for stale node, got %s", status)
	}

	// Verify fresh node is still UP
	err = database.QueryRow("SELECT status FROM nodes WHERE id = ?", "fresh-node").Scan(&status)
	if err != nil {
		t.Fatal(err)
	}
	if status != "UP" {
		t.Errorf("expected status UP for fresh node, got %s", status)
	}
}
