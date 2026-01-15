package controller

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/angariumd/angarium/internal/auth"
	"github.com/angariumd/angarium/internal/db"
	"github.com/angariumd/angarium/internal/models"
)

func TestJobLifecycle(t *testing.T) {
	dbPath := "test_lifecycle.db"
	os.Remove(dbPath)
	defer os.Remove(dbPath)

	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	database.Init()

	server := NewServer(database, auth.NewAuthenticator(database), "test-token")

	// 0. Seed dependent records
	database.Exec("INSERT INTO users (id, name, token_hash) VALUES ('user-1', 'User 1', 'token-1')")
	database.Exec("INSERT INTO nodes (id, status, last_heartbeat_at, addr, agent_version) VALUES ('node-1', 'UP', datetime('now'), 'http://localhost:8081', 'v0.1.0')")
	database.Exec("INSERT INTO gpus (id, node_id, idx, uuid, name, memory_mb, health, last_seen_at) VALUES ('gpu-1', 'node-1', 0, 'gpu-uuid-1', 'RTX 3090', 24576, 'OK', datetime('now'))")

	// 1. Seed a job in ALLOCATED state with a lease
	jobID := "lifecycle-job"
	_, err = database.Exec(`
		INSERT INTO jobs (id, owner_id, state, priority, gpu_count, command, cwd, env_json, created_at, queued_at)
		VALUES (?, 'user-1', ?, 0, 1, 'echo', '/tmp', '{}', datetime('now'), datetime('now'))
	`, jobID, models.JobStateAllocated)
	if err != nil {
		t.Fatal(err)
	}

	allocID := "alloc-1"
	_, err = database.Exec(`
		INSERT INTO allocations (id, job_id, node_id, status, created_at)
		VALUES (?, ?, 'node-1', 'ALLOCATED', datetime('now'))
	`, allocID, jobID)
	if err != nil {
		t.Fatal(err)
	}

	_, err = database.Exec(`
		INSERT INTO gpu_leases (gpu_id, allocation_id, leased_at, expires_at)
		VALUES ('gpu-1', ?, datetime('now'), datetime('now', '+1 minute'))
	`, allocID)
	if err != nil {
		t.Fatal(err)
	}

	// 2. Mock Agent reporting RUNNING
	updateReq := struct {
		State models.JobState `json:"state"`
	}{
		State: models.JobStateRunning,
	}
	body, _ := json.Marshal(updateReq)
	req := httptest.NewRequest("POST", "/v1/agent/jobs/"+jobID+"/state", bytes.NewBuffer(body))
	req.SetPathValue("id", jobID) // Manually set because httptest.NewRequest doesn't use the mux patterns
	rr := httptest.NewRecorder()

	server.handleJobStateUpdate(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}

	// Verify job is RUNNING and has StartedAt
	var state models.JobState
	var startedAt *time.Time
	err = database.QueryRow("SELECT state, started_at FROM jobs WHERE id = ?", jobID).Scan(&state, &startedAt)
	if err != nil {
		t.Fatal(err)
	}
	if state != models.JobStateRunning {
		t.Errorf("expected state RUNNING, got %s", state)
	}
	if startedAt == nil {
		t.Errorf("expected started_at to be set")
	}

	// 3. Mock Agent reporting SUCCEEDED
	exitCode := 0
	finishReq := struct {
		State    models.JobState `json:"state"`
		ExitCode *int            `json:"exit_code"`
	}{
		State:    models.JobStateSucceeded,
		ExitCode: &exitCode,
	}
	body, _ = json.Marshal(finishReq)
	req = httptest.NewRequest("POST", "/v1/agent/jobs/"+jobID+"/state", bytes.NewBuffer(body))
	req.SetPathValue("id", jobID)
	rr = httptest.NewRecorder()

	server.handleJobStateUpdate(rr, req)

	// Verify job is SUCCEEDED and has FinishedAt and ExitCode
	var finishedAt *time.Time
	var resExitCode *int
	err = database.QueryRow("SELECT state, finished_at, exit_code FROM jobs WHERE id = ?", jobID).Scan(&state, &finishedAt, &resExitCode)
	if err != nil {
		t.Fatal(err)
	}
	if state != models.JobStateSucceeded {
		t.Errorf("expected state SUCCEEDED, got %s", state)
	}
	if finishedAt == nil {
		t.Errorf("expected finished_at to be set")
	}
	if resExitCode == nil || *resExitCode != 0 {
		t.Errorf("expected exit_code 0, got %v", resExitCode)
	}

	// Verify lease is cleared
	var leaseCount int
	database.QueryRow("SELECT COUNT(*) FROM gpu_leases WHERE allocation_id = ?", allocID).Scan(&leaseCount)
	if leaseCount != 0 {
		t.Errorf("expected lease to be cleared, got %d", leaseCount)
	}

	// Verify allocation is released
	var allocStatus string
	var releasedAt *time.Time
	database.QueryRow("SELECT status, released_at FROM allocations WHERE id = ?", allocID).Scan(&allocStatus, &releasedAt)
	if allocStatus != string(models.JobStateSucceeded) {
		t.Errorf("expected allocation status SUCCEEDED, got %s", allocStatus)
	}
	if releasedAt == nil {
		t.Errorf("expected released_at to be set")
	}
}

func TestJobCancel(t *testing.T) {
	dbPath := "test_cancel.db"
	os.Remove(dbPath)
	defer os.Remove(dbPath)

	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	database.Init()

	server := NewServer(database, auth.NewAuthenticator(database), "test-token")

	// Seed User
	database.Exec("INSERT INTO users (id, name, token_hash) VALUES ('user-1', 'User 1', 'token-1')")

	// 1. Cancel QUEUED job
	jobID := "queued-job"
	database.Exec(`
		INSERT INTO jobs (id, owner_id, state, priority, gpu_count, command, cwd, env_json, created_at, queued_at)
		VALUES (?, 'user-1', ?, 0, 1, 'echo', '/tmp', '{}', datetime('now'), datetime('now'))
	`, jobID, models.JobStateQueued)

	req := httptest.NewRequest("POST", "/v1/jobs/"+jobID+"/cancel", nil)
	req.SetPathValue("id", jobID)
	rr := httptest.NewRecorder()

	server.handleJobCancel(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}

	var state models.JobState
	database.QueryRow("SELECT state FROM jobs WHERE id = ?", jobID).Scan(&state)
	if state != models.JobStateCanceled {
		t.Errorf("expected state CANCELED, got %s", state)
	}

	// 2. Cancel RUNNING job
	jobID2 := "running-job"
	database.Exec(`
		INSERT INTO jobs (id, owner_id, state, priority, gpu_count, command, cwd, env_json, created_at, queued_at)
		VALUES (?, 'user-1', ?, 0, 1, 'echo', '/tmp', '{}', datetime('now'), datetime('now'))
	`, jobID2, models.JobStateRunning)

	allocID := "alloc-2"
	database.Exec("INSERT INTO nodes (id, status, last_heartbeat_at, addr, agent_version) VALUES ('node-1', 'UP', datetime('now'), 'http://localhost:8081', 'v0.1.0')")
	database.Exec(`
		INSERT INTO allocations (id, job_id, node_id, status, created_at)
		VALUES (?, ?, 'node-1', 'RUNNING', datetime('now'))
	`, allocID, jobID2)

	database.Exec("INSERT INTO gpus (id, node_id, idx, uuid, name, memory_mb, health, last_seen_at) VALUES ('gpu-2', 'node-1', 0, 'gpu-uuid-2', 'RTX 3090', 24576, 'OK', datetime('now'))")
	database.Exec(`
		INSERT INTO gpu_leases (gpu_id, allocation_id, leased_at, expires_at)
		VALUES ('gpu-2', ?, datetime('now'), datetime('now', '+1 minute'))
	`, allocID)

	req = httptest.NewRequest("POST", "/v1/jobs/"+jobID2+"/cancel", nil)
	req.SetPathValue("id", jobID2)
	rr = httptest.NewRecorder()

	server.handleJobCancel(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}

	database.QueryRow("SELECT state FROM jobs WHERE id = ?", jobID2).Scan(&state)
	if state != models.JobStateCanceled {
		t.Errorf("expected state CANCELED, got %s", state)
	}

	var leaseCount int
	database.QueryRow("SELECT COUNT(*) FROM gpu_leases WHERE allocation_id = ?", allocID).Scan(&leaseCount)
	if leaseCount != 0 {
		t.Errorf("expected lease to be cleared, got %d", leaseCount)
	}
}
