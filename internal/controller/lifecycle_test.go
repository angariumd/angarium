package controller

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/angariumd/angarium/internal/auth"
	"github.com/angariumd/angarium/internal/db"
	"github.com/angariumd/angarium/internal/events"
	"github.com/angariumd/angarium/internal/models"
)

func TestJobLifecycle(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test_lifecycle.db")
	log.SetOutput(io.Discard)
	defer log.SetOutput(os.Stderr)

	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	database.Init()

	eventMgr := events.New(database)
	defer eventMgr.Close()

	server := NewServer(database, auth.NewAuthenticator(database), eventMgr, "test-token")

	// Setup basic infrastructure (user + 1 node with 1 GPU)
	database.Exec("INSERT INTO users (id, name, token_hash) VALUES ('user-1', 'User 1', 'token-1')")
	database.Exec("INSERT INTO nodes (id, status, last_heartbeat_at, addr, agent_version) VALUES ('node-1', 'UP', datetime('now'), 'http://localhost:8081', 'v0.1.0')")
	database.Exec("INSERT INTO gpus (id, node_id, idx, uuid, name, memory_mb, health, last_seen_at) VALUES ('gpu-1', 'node-1', 0, 'gpu-uuid-1', 'RTX 3090', 24576, 'OK', datetime('now'))")

	jobID := "lifecycle-job"
	allocID := "alloc-1"

	t.Run("transitions to running", func(t *testing.T) {
		database.Exec(`
			INSERT INTO jobs (id, owner_id, state, priority, gpu_count, command, cwd, env_json, created_at, queued_at)
			VALUES (?, 'user-1', ?, 0, 1, 'echo', '/tmp', '{}', datetime('now'), datetime('now'))
		`, jobID, models.JobStateAllocated)

		database.Exec(`
			INSERT INTO allocations (id, job_id, node_id, status, created_at)
			VALUES (?, ?, 'node-1', 'ALLOCATED', datetime('now'))
		`, allocID, jobID)

		database.Exec(`
			INSERT INTO gpu_leases (gpu_id, allocation_id, leased_at, expires_at)
			VALUES ('gpu-1', ?, datetime('now'), datetime('now', '+1 minute'))
		`, allocID)

		// Agent sends running update
		updateReq := struct {
			State models.JobState `json:"state"`
		}{
			State: models.JobStateRunning,
		}
		body, _ := json.Marshal(updateReq)
		req := httptest.NewRequest("POST", "/v1/agent/jobs/"+jobID+"/state", bytes.NewBuffer(body))
		req.SetPathValue("id", jobID)
		rr := httptest.NewRecorder()

		server.handleJobStateUpdate(rr, req)

		if rr.Code != http.StatusOK {
			t.Errorf("expected status 200, got %d", rr.Code)
		}

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
			t.Errorf("expected started_at field")
		}
	})

	t.Run("transitions to success", func(t *testing.T) {
		exitCode := 0
		finishReq := struct {
			State    models.JobState `json:"state"`
			ExitCode *int            `json:"exit_code"`
		}{
			State:    models.JobStateSucceeded,
			ExitCode: &exitCode,
		}
		body, _ := json.Marshal(finishReq)
		req := httptest.NewRequest("POST", "/v1/agent/jobs/"+jobID+"/state", bytes.NewBuffer(body))
		req.SetPathValue("id", jobID)
		rr := httptest.NewRecorder()

		server.handleJobStateUpdate(rr, req)

		var state models.JobState
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
			t.Errorf("expected finished_at field")
		}
		if resExitCode == nil || *resExitCode != 0 {
			t.Errorf("expected exit_code 0, got %v", resExitCode)
		}

		var leaseCount int
		database.QueryRow("SELECT COUNT(*) FROM gpu_leases WHERE allocation_id = ?", allocID).Scan(&leaseCount)
		if leaseCount != 0 {
			t.Errorf("expected 0 leases, got %d", leaseCount)
		}

		var allocStatus string
		var releasedAt *time.Time
		database.QueryRow("SELECT status, released_at FROM allocations WHERE id = ?", allocID).Scan(&allocStatus, &releasedAt)
		if allocStatus != string(models.JobStateSucceeded) {
			t.Errorf("expected allocation status SUCCEEDED, got %s", allocStatus)
		}
		if releasedAt == nil {
			t.Errorf("expected released_at field")
		}
	})
}

func TestJobCancel(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test_cancel.db")
	log.SetOutput(io.Discard)
	defer log.SetOutput(os.Stderr)

	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	database.Init()

	eventMgr := events.New(database)
	defer eventMgr.Close()

	server := NewServer(database, auth.NewAuthenticator(database), eventMgr, "test-token")

	database.Exec("INSERT INTO users (id, name, token_hash) VALUES ('user-1', 'User 1', 'token-1')")

	t.Run("cancel queued", func(t *testing.T) {
		jobID := "queued-job"
		database.Exec(`
			INSERT INTO jobs (id, owner_id, state, priority, gpu_count, command, cwd, env_json, created_at, queued_at)
			VALUES (?, 'user-1', ?, 0, 1, 'echo', '/tmp', '{}', datetime('now'), datetime('now'))
		`, jobID, models.JobStateQueued)

		req := httptest.NewRequest("POST", "/v1/jobs/"+jobID+"/cancel", nil)
		req.SetPathValue("id", jobID)
		rr := httptest.NewRecorder()

		ctx := auth.ContextWithUser(req.Context(), &models.User{ID: "user-1", Name: "User 1"})
		server.handleJobCancel(rr, req.WithContext(ctx))

		if rr.Code != http.StatusOK {
			t.Errorf("expected status 200, got %d", rr.Code)
		}

		var state models.JobState
		database.QueryRow("SELECT state FROM jobs WHERE id = ?", jobID).Scan(&state)
		if state != models.JobStateCanceled {
			t.Errorf("expected state CANCELED, got %s", state)
		}
	})

	t.Run("cancel running", func(t *testing.T) {
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

		req := httptest.NewRequest("POST", "/v1/jobs/"+jobID2+"/cancel", nil)
		req.SetPathValue("id", jobID2)
		rr := httptest.NewRecorder()

		ctx := auth.ContextWithUser(req.Context(), &models.User{ID: "user-1", Name: "User 1"})
		server.handleJobCancel(rr, req.WithContext(ctx))

		if rr.Code != http.StatusOK {
			t.Errorf("expected status 200, got %d", rr.Code)
		}

		var state models.JobState
		database.QueryRow("SELECT state FROM jobs WHERE id = ?", jobID2).Scan(&state)
		if state != models.JobStateCanceled {
			t.Errorf("expected state CANCELED, got %s", state)
		}

		var leaseCount int
		database.QueryRow("SELECT COUNT(*) FROM gpu_leases WHERE allocation_id = ?", allocID).Scan(&leaseCount)
		if leaseCount != 0 {
			t.Errorf("expected 0 leases, got %d", leaseCount)
		}
	})
}
