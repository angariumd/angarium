package scheduler

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/angariumd/angarium/internal/db"
	"github.com/angariumd/angarium/internal/models"
)

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

	s := New(database)

	// 1. Submit a 2-GPU job. Should go to Node A (Best-fit).
	job1ID := seedJob(t, database, "user-1", 2)
	s.Schedule(context.Background())

	checkJobState(t, database, job1ID, models.JobStateAllocated, "node-A")

	// 2. Submit a 3-GPU job. Should go to Node B (Node A is full).
	job2ID := seedJob(t, database, "user-1", 3)
	s.Schedule(context.Background())

	checkJobState(t, database, job2ID, models.JobStateAllocated, "node-B")

	// 3. Submit a 2-GPU job. Only 1 left on Node B, 0 on Node A. Should stay QUEUED with "Insufficient total".
	job3ID := seedJob(t, database, "user-1", 2)
	s.Schedule(context.Background())

	checkJobQueuedWithReason(t, database, job3ID, "Insufficient total GPUs in cluster (requested 2, total available 1)")

	// 4. Submit a 1-GPU job. Should go to Node B (Node B has 1 left).
	job4ID := seedJob(t, database, "user-1", 1)
	s.Schedule(context.Background())

	checkJobState(t, database, job4ID, models.JobStateAllocated, "node-B")

	// 5. Release Node A GPUs and seed a new node C with 1 GPU.
	// Cluster: Node A (2), Node B (0), Node C (1) - but let's just use Node A and C.
	// Actually, let's just seed a new scenario.
	seedNode(t, database, "node-C", 1)
	// Current State:
	// Actually, let's just create a fresh scenario for "Split" check.

	// Reset All state for a clean split test
	database.Exec("DELETE FROM gpu_leases")
	database.Exec("DELETE FROM allocations")
	database.Exec("DELETE FROM gpus")
	database.Exec("DELETE FROM nodes")
	database.Exec("UPDATE jobs SET state='CANCELED'")

	// Node A: 1 free, Node B: 1 free. Total 2.
	database.Exec("UPDATE gpus SET health='OK'") // reset health
	// We need to manually lease some GPUs to simulate "busy"
	// Or just use the seedNode with 1 GPU each.
	seedNode(t, database, "split-A", 1)
	seedNode(t, database, "split-B", 1)

	jobSplitID := seedJob(t, database, "user-1", 2)
	s.Schedule(context.Background())
	checkJobQueuedWithReason(t, database, jobSplitID, "Insufficient GPUs on a single node (requested 2, max available 1)")
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
	id := "job-" + time.Now().String()
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
