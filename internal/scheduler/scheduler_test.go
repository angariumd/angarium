package scheduler

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/angariumd/angarium/internal/db"
	"github.com/angariumd/angarium/internal/events"
	"github.com/angariumd/angarium/internal/models"
)

func TestLeaseRecovery(t *testing.T) {
	dbPath := "test_recovery.db"
	os.Remove(dbPath)
	defer os.Remove(dbPath)

	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	database.Init()

	seedUser(t, database, "user-1", "Alice", "secret")
	seedNode(t, database, "node-A", 1)

	// Manually insert an expired lease
	// Note: sqlite datetime('now') uses UTC, so we should too.
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

	// On startup, Run() calls CleanupLeases.
	// The GPU should become available.
	s.CleanupLeases(context.Background())

	// Verify lease is gone
	var count int
	database.QueryRow("SELECT COUNT(*) FROM gpu_leases").Scan(&count)
	if count != 0 {
		t.Errorf("Expected 0 leases after cleanup, got %d", count)
	}

	// Try to schedule a new job on that GPU
	jobID := seedJob(t, database, "user-1", 1)
	s.Schedule(context.Background())

	checkJobState(t, database, jobID, models.JobStateAllocated, "node-A")
}

func TestBestFit(t *testing.T) {
	dbPath := "test_scheduler.db"
	os.Remove(dbPath)
	defer os.Remove(dbPath)

	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	database.Init()

	// Seed User
	seedUser(t, database, "user-1", "Alice", "secret")

	// Seed cluster
	// Node A: 2 GPUs
	// Node B: 4 GPUs
	seedNode(t, database, "node-A", 2)
	seedNode(t, database, "node-B", 4)

	eventMgr := events.New(database)
	defer eventMgr.Close()

	s := New(database, eventMgr, "test-token")

	// 1. Submit a 2-GPU job. Should go to Node A (Best-fit).
	job1ID := seedJob(t, database, "user-1", 2)
	s.Schedule(context.Background())

	checkJobState(t, database, job1ID, models.JobStateAllocated, "node-A")

	// 2. Submit a 3-GPU job. Should go to Node B (Node A is full).
	job2ID := seedJob(t, database, "user-1", 3)
	s.Schedule(context.Background())

	checkJobState(t, database, job2ID, models.JobStateAllocated, "node-B")

	// 3. Submit a 2-GPU job. Only 1 left on Node B, 0 on Node A. Should stay QUEUED with "busy" reason.
	job3ID := seedJob(t, database, "user-1", 2)
	s.Schedule(context.Background())

	// Total healthy: 6. Busy: 2 (Job1) + 3 (Job2) = 5. Available: 1.
	// Requested: 2. Reason should be "Waiting for GPUs to be released"
	checkJobQueuedWithReason(t, database, job3ID, "Waiting for GPUs to be released (5 GPUs currently busy/leased)")

	// 4. Submit a 1-GPU job. Should go to Node B (Node B has 1 left).
	job4ID := seedJob(t, database, "user-1", 1)
	s.Schedule(context.Background())

	checkJobState(t, database, job4ID, models.JobStateAllocated, "node-B")

	// 5. Submit a 10-GPU job. Physically impossible.
	job5ID := seedJob(t, database, "user-1", 10)
	s.Schedule(context.Background())
	checkJobQueuedWithReason(t, database, job5ID, "Cluster only has 6 healthy GPUs total, but job requires 10")

	// 6. Submit a 5-GPU job. Cluster has 6 total, but max node is 4. Impossible for single-node placement.
	job6ID := seedJob(t, database, "user-1", 5)
	s.Schedule(context.Background())
	checkJobQueuedWithReason(t, database, job6ID, "No single node has 5 GPUs (max capacity is 4)")

	// 7. Test Fragmentation correctly
	// Reset Everything
	database.Exec("DELETE FROM gpu_leases")
	database.Exec("DELETE FROM allocations")
	database.Exec("DELETE FROM gpus")
	database.Exec("DELETE FROM nodes")
	database.Exec("UPDATE jobs SET state='CANCELED'")

	// Node A: 3 healthy total, Node B: 3 healthy total.
	seedNode(t, database, "frag-A", 3)
	seedNode(t, database, "frag-B", 3)

	// Occupy 2 on A and 2 on B to leave 1 free on each.
	// Total available: 2. Max available single-node: 1.
	jobA := seedJob(t, database, "user-1", 2)
	jobB := seedJob(t, database, "user-1", 2)
	s.Schedule(context.Background())
	checkJobState(t, database, jobA, models.JobStateAllocated, "frag-A")
	checkJobState(t, database, jobB, models.JobStateAllocated, "frag-B")

	// Job needs 2. Total available 2, but split 1+1.
	jobFragID := seedJob(t, database, "user-1", 2)
	s.Schedule(context.Background())
	checkJobQueuedWithReason(t, database, jobFragID, "Waiting for a single node to have 2 free GPUs (fragmented: 2 total free across nodes)")
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

func seedUser(t *testing.T, d *db.DB, id, name, token string) {
	_, err := d.Exec(`
		INSERT INTO users (id, name, token_hash)
		VALUES (?, ?, ?)
	`, id, name, token)
	if err != nil {
		t.Fatal(err)
	}
}
