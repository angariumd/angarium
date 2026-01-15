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
	db         *db.DB
	auth       *auth.Authenticator
	AgentToken string
}

func NewServer(db *db.DB, auth *auth.Authenticator, agentToken string) *Server {
	return &Server{
		db:         db,
		auth:       auth,
		AgentToken: agentToken,
	}
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()

	// Node management (shared token auth)
	mux.Handle("POST /v1/agent/heartbeat", s.auth.AgentMiddleware(s.AgentToken, http.HandlerFunc(s.handleHeartbeat)))
	mux.Handle("POST /v1/agent/jobs/{id}/state", s.auth.AgentMiddleware(s.AgentToken, http.HandlerFunc(s.handleJobStateUpdate)))
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

	// Find nodes that are currently UP but haven't heartbeated recently
	rows, err := s.db.Query("SELECT id FROM nodes WHERE status = 'UP' AND last_heartbeat_at < ?", deadline)
	if err != nil {
		log.Printf("Error querying stale nodes: %v", err)
		return
	}
	defer rows.Close()

	var staleNodeIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err == nil {
			staleNodeIDs = append(staleNodeIDs, id)
		}
	}

	if len(staleNodeIDs) == 0 {
		return
	}

	for _, nodeID := range staleNodeIDs {
		log.Printf("Node %s is stale, marking DOWN and cleaning up jobs", nodeID)
		s.handleNodeOffline(nodeID)
	}
}

func (s *Server) handleNodeOffline(nodeID string) {
	tx, err := s.db.Begin()
	if err != nil {
		log.Printf("Error starting transaction for node %s offline: %v", nodeID, err)
		return
	}
	defer tx.Rollback()

	now := time.Now()

	// 1. Mark node as DOWN
	_, err = tx.Exec("UPDATE nodes SET status = 'DOWN' WHERE id = ?", nodeID)
	if err != nil {
		log.Printf("Error updating node %s status: %v", nodeID, err)
		return
	}

	// 2. Find jobs on this node that are not in terminal state
	// Transition them to LOST
	_, err = tx.Exec(`
		UPDATE jobs SET state = ?, finished_at = ?, reason = 'Node timeout'
		WHERE id IN (
			SELECT job_id FROM allocations 
			WHERE node_id = ? AND released_at IS NULL
		) AND state NOT IN (?, ?, ?)
	`, models.JobStateLost, now, nodeID, models.JobStateSucceeded, models.JobStateFailed, models.JobStateCanceled)
	if err != nil {
		log.Printf("Error updating jobs for stale node %s: %v", nodeID, err)
		return
	}

	// 3. Clear GPU leases for this node
	_, err = tx.Exec(`
		DELETE FROM gpu_leases 
		WHERE allocation_id IN (SELECT id FROM allocations WHERE node_id = ? AND released_at IS NULL)
	`, nodeID)
	if err != nil {
		log.Printf("Error clearing leases for stale node %s: %v", nodeID, err)
		return
	}

	// 4. Mark allocations as released/LOST
	_, err = tx.Exec(`
		UPDATE allocations SET status = 'LOST', released_at = ?
		WHERE node_id = ? AND released_at IS NULL
	`, now, nodeID)
	if err != nil {
		log.Printf("Error updating allocations for stale node %s: %v", nodeID, err)
		return
	}

	if err := tx.Commit(); err != nil {
		log.Printf("Error committing node offline cleanup for %s: %v", nodeID, err)
	}
}

func (s *Server) StartReconciliationLoop(interval time.Duration) {
	ticker := time.NewTicker(interval)
	go func() {
		for range ticker.C {
			s.reconcileCluster()
		}
	}()
}

func (s *Server) reconcileCluster() {
	rows, err := s.db.Query("SELECT id, addr FROM nodes WHERE status = 'UP'")
	if err != nil {
		log.Printf("Reconciler: error querying nodes: %v", err)
		return
	}
	defer rows.Close()

	for rows.Next() {
		var id, addr string
		if err := rows.Scan(&id, &addr); err != nil {
			continue
		}
		if addr != "" {
			go s.reconcileNode(id, addr)
		}
	}
}

func (s *Server) reconcileNode(nodeID, addr string) {
	req, _ := http.NewRequest("GET", addr+"/v1/agent/running", nil)
	req.Header.Set("X-Agent-Token", s.AgentToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("Reconciler: failed to contact agent %s at %s: %v", nodeID, addr, err)
		return
	}
	defer resp.Body.Close()

	var agentJobs []struct {
		JobID string `json:"job_id"`
		PID   int    `json:"pid"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&agentJobs); err != nil {
		log.Printf("Reconciler: failed to decode agent %s response: %v", nodeID, err)
		return
	}

	agentJobIDs := make(map[string]bool)
	for _, j := range agentJobs {
		agentJobIDs[j.JobID] = true
	}

	dbRows, err := s.db.Query(`
		SELECT j.id FROM jobs j
		JOIN allocations a ON a.job_id = j.id
		WHERE a.node_id = ? AND a.released_at IS NULL AND j.state IN (?, ?)
	`, nodeID, models.JobStateRunning, models.JobStateStarting)
	if err != nil {
		log.Printf("Reconciler: error querying DB for node %s: %v", nodeID, err)
		return
	}
	defer dbRows.Close()

	for dbRows.Next() {
		var jobID string
		if err := dbRows.Scan(&jobID); err != nil {
			continue
		}

		if !agentJobIDs[jobID] {
			log.Printf("Reconciler: job %s is orphan on node %s, marking LOST", jobID, nodeID)
			s.markJobLost(jobID, "Process disappeared from agent")
		}
		delete(agentJobIDs, jobID)
	}

	for jobID := range agentJobIDs {
		log.Printf("Reconciler: job %s is zombie on node %s, killing", jobID, nodeID)
		s.killZombie(addr, jobID)
	}
}

func (s *Server) markJobLost(jobID string, reason string) {
	tx, err := s.db.Begin()
	if err != nil {
		return
	}
	defer tx.Rollback()

	now := time.Now()
	_, _ = tx.Exec("UPDATE jobs SET state = ?, finished_at = ?, reason = ? WHERE id = ?", models.JobStateLost, now, reason, jobID)
	_, _ = tx.Exec(`
		DELETE FROM gpu_leases 
		WHERE allocation_id IN (SELECT id FROM allocations WHERE job_id = ?)
	`, jobID)
	_, _ = tx.Exec("UPDATE allocations SET status = 'LOST', released_at = ? WHERE job_id = ?", now, jobID)

	tx.Commit()
}

func (s *Server) killZombie(addr, jobID string) {
	url := addr + "/v1/agent/terminate"
	body, _ := json.Marshal(map[string]string{"job_id": jobID})
	req, _ := http.NewRequest("POST", url, bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Agent-Token", s.AgentToken)
	http.DefaultClient.Do(req)
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

	query += " WHERE id = ? AND state NOT IN (?, ?, ?, ?)"
	args = append(args, jobID, models.JobStateSucceeded, models.JobStateFailed, models.JobStateCanceled, models.JobStateLost)

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
	user := auth.UserFromContext(r.Context())
	if user == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	jobID := r.PathValue("id")
	follow := r.URL.Query().Get("follow")

	// Verify ownership
	var ownerID string
	err := s.db.QueryRow("SELECT owner_id FROM jobs WHERE id = ?", jobID).Scan(&ownerID)
	if err != nil {
		http.Error(w, "job not found", http.StatusNotFound)
		return
	}
	if ownerID != user.ID {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	// Find node address
	var addr string
	err = s.db.QueryRow(`
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

	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("X-Agent-Token", s.AgentToken)
	resp, err := http.DefaultClient.Do(req)
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
	user := auth.UserFromContext(r.Context())
	if user == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	jobID := r.PathValue("id")

	// Verify ownership
	var ownerID string
	var state models.JobState
	err := s.db.QueryRow("SELECT owner_id, state FROM jobs WHERE id = ?", jobID).Scan(&ownerID, &state)
	if err != nil {
		http.Error(w, "job not found", http.StatusNotFound)
		return
	}

	if ownerID != user.ID {
		http.Error(w, "forbidden", http.StatusForbidden)
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

	// 1. Update job record and release resources FIRST to block races
	log.Printf("Canceling job %s (owner: %s, state: %s)", jobID, user.ID, state)
	s.markJobCanceled(jobID)

	if addr != "" {
		// 2. Notify agent SECOND
		go func() {
			url := fmt.Sprintf("%s/v1/agent/terminate", addr)
			reqBody := map[string]string{"job_id": jobID}
			body, _ := json.Marshal(reqBody)
			req, _ := http.NewRequest("POST", url, bytes.NewBuffer(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Agent-Token", s.AgentToken)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				log.Printf("Cancel notify failed for %s: %v", jobID, err)
				return
			}
			resp.Body.Close()
		}()
	}

	w.WriteHeader(http.StatusOK)
}

func (s *Server) markJobCanceled(jobID string) {
	tx, err := s.db.Begin()
	if err != nil {
		log.Printf("Error starting transaction for cancel %s: %v", jobID, err)
		return
	}
	defer tx.Rollback()

	now := time.Now()
	// Overwrite any non-terminal state
	_, err = tx.Exec("UPDATE jobs SET state = ?, finished_at = ? WHERE id = ?", models.JobStateCanceled, now, jobID)
	if err != nil {
		log.Printf("Error updating job %s to CANCELED: %v", jobID, err)
		return
	}

	_, _ = tx.Exec(`
		DELETE FROM gpu_leases 
		WHERE allocation_id IN (SELECT id FROM allocations WHERE job_id = ?)
	`, jobID)
	_, _ = tx.Exec("UPDATE allocations SET status = ?, released_at = ? WHERE job_id = ?", string(models.JobStateCanceled), now, jobID)

	if err := tx.Commit(); err != nil {
		log.Printf("Error committing cancel for %s: %v", jobID, err)
	}
}
