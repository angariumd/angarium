package controller

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/angariumd/angarium/internal/auth"
	"github.com/angariumd/angarium/internal/db"
	"github.com/angariumd/angarium/internal/models"
	"github.com/google/uuid"
)

type Server struct {
	db   *db.DB
	auth *auth.Authenticator
}

func NewServer(db *db.DB, auth *auth.Authenticator) *Server {
	return &Server{
		db:   db,
		auth: auth,
	}
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()

	// Node management
	mux.HandleFunc("POST /v1/agent/heartbeat", s.handleHeartbeat)
	mux.HandleFunc("POST /v1/agent/jobs/{id}/state", s.handleJobStateUpdate)
	mux.Handle("GET /v1/nodes", s.auth.Middleware(http.HandlerFunc(s.handleNodeList)))

	// User job control
	mux.Handle("POST /v1/jobs", s.auth.Middleware(http.HandlerFunc(s.handleJobSubmit)))
	mux.Handle("GET /v1/jobs", s.auth.Middleware(http.HandlerFunc(s.handleJobList)))
	mux.Handle("GET /v1/jobs/{id}/logs", s.auth.Middleware(http.HandlerFunc(s.handleJobLogs)))
	mux.Handle("POST /v1/jobs/{id}/cancel", s.auth.Middleware(http.HandlerFunc(s.handleJobCancel)))

	return mux
}

func (s *Server) StartStaleNodeDetector(interval time.Duration) {
	ticker := time.NewTicker(interval)
	go func() {
		for range ticker.C {
			s.detectStaleNodes()
		}
	}()
}

func (s *Server) detectStaleNodes() {
	deadline := time.Now().Add(-20 * time.Second)
	_, err := s.db.Exec(`
		UPDATE nodes SET status = 'DOWN'
		WHERE last_heartbeat_at < ? AND status = 'UP'
	`, deadline)
	if err != nil {
		fmt.Printf("Error detecting stale nodes: %v\n", err)
	}
}

type HeartbeatRequest struct {
	NodeID       string       `json:"node_id"`
	AgentVersion string       `json:"agent_version"`
	Addr         string       `json:"addr"`
	GPUs         []models.GPU `json:"gpus"`
}

func (s *Server) handleHeartbeat(w http.ResponseWriter, r *http.Request) {
	var req HeartbeatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}

	now := time.Now()

	// Update node
	_, err := s.db.Exec(`
		INSERT INTO nodes (id, status, last_heartbeat_at, agent_version, addr)
		VALUES (?, 'UP', ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			status = 'UP',
			last_heartbeat_at = excluded.last_heartbeat_at,
			agent_version = excluded.agent_version,
			addr = excluded.addr
	`, req.NodeID, now, req.AgentVersion, req.Addr)
	if err != nil {
		http.Error(w, "db error node update", http.StatusInternalServerError)
		return
	}

	// Update GPUs
	for _, gpu := range req.GPUs {
		_, err := s.db.Exec(`
			INSERT INTO gpus (id, node_id, idx, uuid, name, memory_mb, health, last_seen_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET
				idx = excluded.idx,
				uuid = excluded.uuid,
				name = excluded.name,
				memory_mb = excluded.memory_mb,
				health = excluded.health,
				last_seen_at = excluded.last_seen_at
		`, fmt.Sprintf("%s-%s", req.NodeID, gpu.UUID), req.NodeID, gpu.Idx, gpu.UUID, gpu.Name, gpu.MemoryMB, gpu.Health, now)
		if err != nil {
			http.Error(w, "db error gpu update", http.StatusInternalServerError)
			return
		}
	}

	w.WriteHeader(http.StatusOK)
}

type JobSubmitRequest struct {
	GPUCount int               `json:"gpu_count"`
	Priority int               `json:"priority"`
	Command  string            `json:"command"`
	CWD      string            `json:"cwd"`
	Env      map[string]string `json:"env"`
}

func (s *Server) handleJobSubmit(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	if user == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	var req JobSubmitRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}

	if req.GPUCount <= 0 || req.Command == "" || req.CWD == "" {
		http.Error(w, "gpu_count, command, and cwd are required", http.StatusBadRequest)
		return
	}

	envJSON, _ := json.Marshal(req.Env)
	jobID := uuid.New().String()
	now := time.Now()

	_, err := s.db.Exec(`
		INSERT INTO jobs (id, owner_id, state, priority, gpu_count, command, cwd, env_json, created_at, queued_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, jobID, user.ID, models.JobStateQueued, req.Priority, req.GPUCount, req.Command, req.CWD, string(envJSON), now, now)
	if err != nil {
		http.Error(w, "db error job insert", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"id": jobID})
}

func (s *Server) handleJobList(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.Query(`
		SELECT id, owner_id, state, priority, gpu_count, command, cwd, created_at, started_at, finished_at, exit_code, reason 
		FROM jobs ORDER BY created_at DESC
	`)
	if err != nil {
		log.Printf("handleJobList error: %v", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var jobs []models.Job
	for rows.Next() {
		var j models.Job
		if err := rows.Scan(&j.ID, &j.OwnerID, &j.State, &j.Priority, &j.GPUCount, &j.Command, &j.CWD, &j.CreatedAt, &j.StartedAt, &j.FinishedAt, &j.ExitCode, &j.Reason); err != nil {
			http.Error(w, "db error scan", http.StatusInternalServerError)
			return
		}
		jobs = append(jobs, j)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(jobs)
}

func (s *Server) handleNodeList(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.Query(`
		SELECT n.id, n.status, n.last_heartbeat_at, n.agent_version, n.addr,
		(SELECT COUNT(*) FROM gpus g WHERE g.node_id = n.id AND g.health = 'OK') as gpu_count,
		(SELECT COUNT(*) FROM gpus g WHERE g.node_id = n.id AND g.health = 'OK' AND g.id NOT IN (SELECT gpu_id FROM gpu_leases l WHERE l.expires_at > datetime('now'))) as gpu_free
		FROM nodes n
	`)
	if err != nil {
		log.Printf("handleNodeList error: %v", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var nodes []models.Node
	for rows.Next() {
		var n models.Node
		if err := rows.Scan(&n.ID, &n.Status, &n.LastHeartbeatAt, &n.AgentVersion, &n.Addr, &n.GPUCount, &n.GPUFree); err != nil {
			http.Error(w, "db error scan", http.StatusInternalServerError)
			return
		}
		nodes = append(nodes, n)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(nodes)
}

func (s *Server) handleJobStateUpdate(w http.ResponseWriter, r *http.Request) {
	jobID := r.PathValue("id")
	var req struct {
		State    models.JobState `json:"state"`
		ExitCode *int            `json:"exit_code,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}

	tx, err := s.db.Begin()
	if err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()

	now := time.Now()

	// 1. Update Job
	query := "UPDATE jobs SET state = ?"
	args := []any{req.State}

	if req.State == models.JobStateStarting || req.State == models.JobStateRunning {
		query += ", started_at = COALESCE(started_at, ?)"
		args = append(args, now)
	}
	if req.State == models.JobStateSucceeded || req.State == models.JobStateFailed || req.State == models.JobStateCanceled {
		query += ", finished_at = ?, exit_code = ?"
		args = append(args, now, req.ExitCode)
	}

	query += " WHERE id = ?"
	args = append(args, jobID)

	if _, err := tx.Exec(query, args...); err != nil {
		http.Error(w, "db error job update", http.StatusInternalServerError)
		return
	}

	// 2. Clear leases if terminal
	if req.State == models.JobStateSucceeded || req.State == models.JobStateFailed || req.State == models.JobStateCanceled {
		_, err = tx.Exec(`
			DELETE FROM gpu_leases 
			WHERE allocation_id IN (SELECT id FROM allocations WHERE job_id = ?)
		`, jobID)
		if err != nil {
			http.Error(w, "db error lease cleanup", http.StatusInternalServerError)
			return
		}

		_, err = tx.Exec("UPDATE allocations SET status = ?, released_at = ? WHERE job_id = ?", string(req.State), now, jobID)
		if err != nil {
			http.Error(w, "db error allocation update", http.StatusInternalServerError)
			return
		}
	}

	if err := tx.Commit(); err != nil {
		http.Error(w, "db error commit", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleJobLogs(w http.ResponseWriter, r *http.Request) {
	jobID := r.PathValue("id")
	follow := r.URL.Query().Get("follow")

	// Find node address
	var addr string
	err := s.db.QueryRow(`
		SELECT n.addr FROM nodes n
		JOIN allocations a ON a.node_id = n.id
		WHERE a.job_id = ?
	`, jobID).Scan(&addr)
	if err != nil {
		http.Error(w, "job allocation not found", http.StatusNotFound)
		return
	}

	if addr == "" {
		http.Error(w, "node address not found", http.StatusInternalServerError)
		return
	}

	// Proxy to agent
	url := fmt.Sprintf("%s/v1/agent/jobs/%s/logs", addr, jobID)
	if follow == "true" {
		url += "?follow=true"
	}

	resp, err := http.Get(url)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to contact agent: %v", err), http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		http.Error(w, "agent failed to serve logs", resp.StatusCode)
		return
	}

	w.Header().Set("Content-Type", "text/plain")
	if follow == "true" {
		w.Header().Set("Transfer-Encoding", "chunked")
	}

	// Stream from agent to client
	_, _ = io.Copy(w, resp.Body)
}

func (s *Server) handleJobCancel(w http.ResponseWriter, r *http.Request) {
	jobID := r.PathValue("id")

	// 1. Get job state
	var state models.JobState
	err := s.db.QueryRow("SELECT state FROM jobs WHERE id = ?", jobID).Scan(&state)
	if err != nil {
		http.Error(w, "job not found", http.StatusNotFound)
		return
	}

	if state == models.JobStateSucceeded || state == models.JobStateFailed || state == models.JobStateCanceled {
		http.Error(w, "job is already terminal", http.StatusBadRequest)
		return
	}

	if state == models.JobStateQueued {
		// Cancel immediately if not yet allocated
		_, err := s.db.Exec("UPDATE jobs SET state = ? WHERE id = ?", models.JobStateCanceled, jobID)
		if err != nil {
			http.Error(w, "db error", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		return
	}

	// For ALLOCATED, STARTING, RUNNING: tell the agent
	var addr string
	err = s.db.QueryRow(`
		SELECT n.addr FROM nodes n
		JOIN allocations a ON a.node_id = n.id
		WHERE a.job_id = ?
	`, jobID).Scan(&addr)
	if err != nil {
		// If no allocation found but not QUEUED, something is wrong, but let's just mark canceled
		_, _ = s.db.Exec("UPDATE jobs SET state = ? WHERE id = ?", models.JobStateCanceled, jobID)
		w.WriteHeader(http.StatusOK)
		return
	}

	if addr != "" {
		// Notify agent
		url := fmt.Sprintf("%s/v1/agent/terminate", addr)
		reqBody := map[string]string{"job_id": jobID}
		body, _ := json.Marshal(reqBody)
		http.Post(url, "application/json", bytes.NewBuffer(body))
	}

	// Update job record and release resources
	s.markJobCanceled(jobID)

	w.WriteHeader(http.StatusOK)
}

func (s *Server) markJobCanceled(jobID string) {
	tx, err := s.db.Begin()
	if err != nil {
		return
	}
	defer tx.Rollback()

	now := time.Now()
	_, _ = tx.Exec("UPDATE jobs SET state = ?, finished_at = ? WHERE id = ?", models.JobStateCanceled, now, jobID)
	_, _ = tx.Exec(`
		DELETE FROM gpu_leases 
		WHERE allocation_id IN (SELECT id FROM allocations WHERE job_id = ?)
	`, jobID)
	_, _ = tx.Exec("UPDATE allocations SET status = ?, released_at = ? WHERE job_id = ?", string(models.JobStateCanceled), now, jobID)

	tx.Commit()
}
