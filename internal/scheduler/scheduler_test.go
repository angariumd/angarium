package scheduler

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
	"github.com/angariumd/angarium/internal/models"
)

func TestLeaseRecovery(t *testing.T) {
	dbPath := filepath.Join(os.TempDir(), fmt.Sprintf("test_recovery_%d.db", time.Now().UnixNano()))
	defer os.Remove(dbPath)
	log.SetOutput(io.Discard)
	defer log.SetOutput(os.Stderr)

	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	database.Init()

	seedUser(t, database, "user-1", "Alice", "secret")
	seedNode(t, database, "node-A", 1)

	t.Run("startup recovery", func(t *testing.T) {
		database.Exec(`
			INSERT INTO allocations (id, job_id, node_id, status, created_at)
			VALUES ('old-alloc', 'old-job', 'node-A', 'ALLOCATED', datetime('now', '-2 minutes'))
		`)
		database.Exec(`
			INSERT INTO gpu_leases (gpu_id, allocation_id, leased_at, expires_at)
			VALUES ('GPU-node-A-0', 'old-alloc', datetime('now', '-2 minutes'), datetime('now', '-1 minute'))
		`)

		eventMgr := events.New(database)
		defer eventMgr.Close()

		s := New(database, eventMgr, "test-token")
		s.CleanupLeases()

		var count int
		database.QueryRow("SELECT COUNT(*) FROM gpu_leases").Scan(&count)
		if count != 0 {
			t.Errorf("expected 0 leases, got %d", count)
		}

		jobID := seedJob(t, database, "user-1", 1)
		s.Schedule()
		checkJobState(t, database, jobID, models.JobStateAllocated, "node-A")
	})
}

func TestBestFit(t *testing.T) {
	dbPath := filepath.Join(os.TempDir(), fmt.Sprintf("test_scheduler_%d.db", time.Now().UnixNano()))
	defer os.Remove(dbPath)
	log.SetOutput(io.Discard)
	defer log.SetOutput(os.Stderr)

	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	database.Init()

	seedUser(t, database, "user-1", "Alice", "secret")
	seedNode(t, database, "node-A", 2)
	seedNode(t, database, "node-B", 4)

	eventMgr := events.New(database)
	defer eventMgr.Close()
	s := New(database, eventMgr, "test-token")

	t.Run("best-fit packing", func(t *testing.T) {
		// Allocate 2 GPUs on Node A
		job1ID := seedJob(t, database, "user-1", 2)
		s.Schedule()
		checkJobState(t, database, job1ID, models.JobStateAllocated, "node-A")

		// Allocate 3 GPUs on Node B
		job2ID := seedJob(t, database, "user-1", 3)
		s.Schedule()
		checkJobState(t, database, job2ID, models.JobStateAllocated, "node-B")
	})

	t.Run("insufficient capacity", func(t *testing.T) {
		// Clean start
		database.Exec("DELETE FROM gpu_leases")
		database.Exec("DELETE FROM allocations")
		database.Exec("DELETE FROM gpus")
		database.Exec("DELETE FROM nodes")
		database.Exec("DELETE FROM jobs")

		seedNode(t, database, "node-A", 2)
		seedNode(t, database, "node-B", 4)

		// Job 1: 2 GPUs -> node-A (2/2)
		job1ID := seedJob(t, database, "user-1", 2)
		s.Schedule()
		checkJobState(t, database, job1ID, models.JobStateAllocated, "node-A")

		// Job 2: 3 GPUs -> node-B (3/4)
		job2ID := seedJob(t, database, "user-1", 3)
		s.Schedule()
		checkJobState(t, database, job2ID, models.JobStateAllocated, "node-B")

		// Available: 1 GPU on node-B. Total Busy: 5.
		// Job 3 (2 GPUs) -> Should wait
		job3ID := seedJob(t, database, "user-1", 2)
		s.Schedule()
		checkJobQueuedWithReason(t, database, job3ID, "waiting for GPUs (5 busy)")

		// Request more than cluster total
		job4ID := seedJob(t, database, "user-1", 10)
		s.Schedule()
		checkJobQueuedWithReason(t, database, job4ID, "insufficient cluster capacity: 6/10")

		// Request more than largest node EVER registered
		job5ID := seedJob(t, database, "user-1", 5)
		s.Schedule()
		checkJobFailedWithReason(t, database, job5ID, "unschedulable: requested 5 GPUs but largest node has 4")

		// Request fits in a registered node, but no UP node is big enough
		// Add another small node so total capacity is enough, but max healthy node is still 2
		seedNode(t, database, "node-C", 2)
		// Mark node-B (4 GPUs) OFFLINE
		database.Exec("UPDATE nodes SET status = 'OFFLINE' WHERE id = 'node-B'")

		// Healthy nodes: node-A(2), node-C(2). Total Healthy = 4. Max Healthy = 2.
		// Max Registered = 4 (from node-B).
		// Job 6 (3 GPUs) -> Should be queued with "no node healthy"
		job6ID := seedJob(t, database, "user-1", 3)
		s.Schedule()
		checkJobQueuedWithReason(t, database, job6ID, "no node currently has 3 healthy GPUs (max 2)")
	})

	t.Run("fragmentation", func(t *testing.T) {
		database.Exec("DELETE FROM gpu_leases")
		database.Exec("DELETE FROM allocations")
		database.Exec("DELETE FROM gpus")
		database.Exec("DELETE FROM nodes")
		database.Exec("UPDATE jobs SET state='CANCELED'")

		seedNode(t, database, "frag-A", 3)
		seedNode(t, database, "frag-B", 3)

		// Allocate 2 GPUs on each node
		seedJob(t, database, "user-1", 2)
		seedJob(t, database, "user-1", 2)
		s.Schedule()

		// Request 2 GPUs when fragmented across nodes
		jobFragID := seedJob(t, database, "user-1", 2)
		s.Schedule()
		checkJobQueuedWithReason(t, database, jobFragID, "fragmented: 2 free total, but none fit 2")
	})
}

func seedNode(t *testing.T, d *db.DB, id string, gpuCount int) {
	_, err := d.Exec(`
		INSERT INTO nodes (id, status, last_heartbeat_at, agent_version, addr)
		VALUES (?, 'UP', ?, 'v0.1.0', 'http://localhost:8081')
	`, id, time.Now())
	if err != nil {
		t.Fatal(err)
	}

	for i := 0; i < gpuCount; i++ {
		uuid := fmt.Sprintf("GPU-%s-%d", id, i)
		_, err := d.Exec(`
			INSERT INTO gpus (id, node_id, idx, uuid, name, memory_mb, health, last_seen_at)
			VALUES (?, ?, ?, ?, 'A100', 40960, 'OK', ?)
		`, uuid, id, i, uuid, time.Now())
		if err != nil {
			t.Fatal(err)
		}
	}
}

func seedJob(t *testing.T, d *db.DB, owner string, gpus int) string {
	id := fmt.Sprintf("job-%d", time.Now().UnixNano())
	_, err := d.Exec(`
		INSERT INTO jobs (id, owner_id, state, priority, gpu_count, command, cwd, env_json, created_at, queued_at)
		VALUES (?, ?, ?, 0, ?, 'echo', '/tmp', '{}', ?, ?)
	`, id, owner, models.JobStateQueued, gpus, time.Now(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func checkJobState(t *testing.T, d *db.DB, jobID string, expectedState models.JobState, expectedNode string) {
	var state models.JobState
	err := d.QueryRow("SELECT state FROM jobs WHERE id = ?", jobID).Scan(&state)
	if err != nil {
		t.Fatal(err)
	}
	if state != expectedState {
		t.Errorf("Expected state %s, got %s", expectedState, state)
	}

	if expectedNode != "" {
		var nodeID string
		err := d.QueryRow("SELECT node_id FROM allocations WHERE job_id = ?", jobID).Scan(&nodeID)
		if err != nil {
			t.Fatal(err)
		}
		if nodeID != expectedNode {
			t.Errorf("Expected node %s, got %s", expectedNode, nodeID)
		}
	}
}

func checkJobQueuedWithReason(t *testing.T, d *db.DB, jobID string, expectedReason string) {
	var state models.JobState
	var reason *string
	err := d.QueryRow("SELECT state, reason FROM jobs WHERE id = ?", jobID).Scan(&state, &reason)
	if err != nil {
		t.Fatal(err)
	}
	if state != models.JobStateQueued {
		t.Errorf("Expected state QUEUED, got %s", state)
	}
	if reason == nil {
		t.Fatalf("Expected reason, got nil")
	}
	if *reason != expectedReason {
		t.Errorf("Expected reason '%s', got '%s'", expectedReason, *reason)
	}
}

func checkJobFailedWithReason(t *testing.T, d *db.DB, jobID string, expectedReason string) {
	var state models.JobState
	var reason *string
	err := d.QueryRow("SELECT state, reason FROM jobs WHERE id = ?", jobID).Scan(&state, &reason)
	if err != nil {
		t.Fatal(err)
	}
	if state != models.JobStateFailed {
		t.Errorf("Expected state FAILED, got %s", state)
	}
	if reason == nil {
		t.Fatalf("Expected reason, got nil")
	}
	if *reason != expectedReason {
		t.Errorf("Expected reason '%s', got '%s'", expectedReason, *reason)
	}
}

func seedUser(t *testing.T, d *db.DB, id, name, token string) {
	_, err := d.Exec(`
		INSERT INTO users (id, name, token_hash)
		VALUES (?, ?, ?)
	`, id, name, auth.HashToken(token))
	if err != nil {
		t.Fatal(err)
	}
}
