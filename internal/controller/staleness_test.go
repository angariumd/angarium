package controller

import (
	"os"
	"testing"

	"github.com/angariumd/angarium/internal/auth"
	"github.com/angariumd/angarium/internal/db"
	"github.com/angariumd/angarium/internal/events"
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

	eventMgr := events.New(database)
	defer eventMgr.Close()

	server := NewServer(database, auth.NewAuthenticator(database), eventMgr, "test-token")

	// Insert a node that is currently UP but staleness is imminent
	_, err = database.Exec(`
		INSERT INTO nodes (id, status, last_heartbeat_at, agent_version)
		VALUES (?, 'UP', datetime('now', '-30 seconds'), ?)
	`, "stale-node", "v0.1.0")
	if err != nil {
		t.Fatal(err)
	}

	// Insert a fresh node
	_, err = database.Exec(`
		INSERT INTO nodes (id, status, last_heartbeat_at, agent_version)
		VALUES (?, 'UP', datetime('now'), ?)
	`, "fresh-node", "v0.1.0")
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
