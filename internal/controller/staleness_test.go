package controller

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/angariumd/angarium/internal/auth"
	"github.com/angariumd/angarium/internal/db"
	"github.com/angariumd/angarium/internal/events"
)

func TestNodeStaleness(t *testing.T) {
	dbPath := filepath.Join(os.TempDir(), fmt.Sprintf("test_staleness_%d.db", time.Now().UnixNano()))
	log.SetOutput(io.Discard)
	defer log.SetOutput(os.Stderr)
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

	database.Exec(`
		INSERT INTO nodes (id, status, last_heartbeat_at, agent_version)
		VALUES (?, 'UP', datetime('now', '-30 seconds'), ?)
	`, "stale-node", "v0.1.0")

	database.Exec(`
		INSERT INTO nodes (id, status, last_heartbeat_at, agent_version)
		VALUES (?, 'UP', datetime('now'), ?)
	`, "fresh-node", "v0.1.0")

	server.detectStaleNodes()

	var status string
	database.QueryRow("SELECT status FROM nodes WHERE id = ?", "stale-node").Scan(&status)
	if status != "DOWN" {
		t.Errorf("expected status DOWN for stale node, got %s", status)
	}

	database.QueryRow("SELECT status FROM nodes WHERE id = ?", "fresh-node").Scan(&status)
	if status != "UP" {
		t.Errorf("expected status UP for fresh node, got %s", status)
	}
}
