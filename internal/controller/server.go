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
	"github.com/angariumd/angarium/internal/events"
	"github.com/angariumd/angarium/internal/models"
	"github.com/google/uuid"
)

const SQLTimeLayout = "2006-01-02 15:04:05"

func formatTime(t time.Time) string {
	return t.UTC().Format(SQLTimeLayout)
}

type Server struct {
	db         *db.DB
	auth       *auth.Authenticator
	events     *events.EventManager
	AgentToken string
}

func NewServer(db *db.DB, auth *auth.Authenticator, events *events.EventManager, agentToken string) *Server {
	return &Server{
		db:         db,
		auth:       auth,
		events:     events,
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
	mux.Handle("GET /v1/jobs/{id}/events", s.auth.Middleware(http.HandlerFunc(s.handleJobEvents)))
	mux.Handle("POST /v1/jobs/{id}/cancel", s.auth.Middleware(http.HandlerFunc(s.handleJobCancel)))
	mux.Handle("GET /v1/whoami", s.auth.Middleware(http.HandlerFunc(s.handleWhoami)))

	return s.withCORS(mux)
}

func (s *Server) withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Agent-Token")

		if r.Method == "OPTIONS" {
			log.Printf("CORS: Handling preflight for %s", r.URL.Path)
			w.WriteHeader(http.StatusOK)
			return
		}

		next.ServeHTTP(w, r)
	})
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
	now := time.Now().UTC()
	deadline := now.Add(-20 * time.Second)

	// find stale nodes
	rows, err := s.db.Query("SELECT id FROM nodes WHERE status = 'UP' AND last_heartbeat_at < ?", formatTime(deadline))
	if err != nil {
		log.Printf("failed to query stale nodes: %v", err)
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
		log.Printf("Node %s is stale, marking DOWN", nodeID)
		s.handleNodeOffline(nodeID)
	}
}

func (s *Server) handleNodeOffline(nodeID string) {
	tx, err := s.db.Begin()
	if err != nil {
		log.Printf("failed to offline node %s: %v", nodeID, err)
		return
	}
	defer tx.Rollback()

	now := time.Now().UTC()
	nowStr := formatTime(now)

	// 1. Mark node as DOWN
	_, err = tx.Exec("UPDATE nodes SET status = 'DOWN' WHERE id = ?", nodeID)
	if err != nil {
		log.Printf("Error updating node %s status: %v", nodeID, err)
		return
	}
	s.events.Emit(events.TypeNodeOffline, nil, &nodeID, nil)

	// Find active jobs on this node
	rows, err := tx.Query(`
		SELECT j.id FROM jobs j
		JOIN allocations a ON a.job_id = j.id
		WHERE a.node_id = ? AND a.released_at IS NULL AND j.state NOT IN (?, ?, ?)
	`, nodeID, models.JobStateSucceeded, models.JobStateFailed, models.JobStateCanceled)
	if err != nil {
		log.Printf("Error querying jobs for stale node %s: %v", nodeID, err)
		return
	}
	defer rows.Close()

	var jobIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err == nil {
			jobIDs = append(jobIDs, id)
		}
	}

	for _, id := range jobIDs {
		_, err = tx.Exec("UPDATE jobs SET state = ?, finished_at = ?, reason = 'Node timeout' WHERE id = ?", models.JobStateLost, nowStr, id)
		if err != nil {
			log.Printf("Error updating job %s to LOST for stale node %s: %v", id, nodeID, err)
		}
		s.events.Emit(events.TypeJobLost, &id, &nodeID, map[string]string{"reason": "node_offline"})
		_, err = tx.Exec("UPDATE allocations SET status = 'LOST', released_at = ? WHERE job_id = ? AND released_at IS NULL", nowStr, id)
		if err != nil {
			log.Printf("Error updating allocation for job %s to LOST for stale node %s: %v", id, nodeID, err)
		}
		_, err = tx.Exec("DELETE FROM gpu_leases WHERE allocation_id IN (SELECT id FROM allocations WHERE job_id = ?)", id)
		if err != nil {
			log.Printf("Error clearing leases for job %s for stale node %s: %v", id, nodeID, err)
		}
	}

	// Mark remaining allocations as LOST
	_, err = tx.Exec(`
		UPDATE allocations SET status = 'LOST', released_at = ?
		WHERE node_id = ? AND released_at IS NULL
	`, nowStr, nodeID)
	if err != nil {
		log.Printf("Error updating remaining allocations for stale node %s: %v", nodeID, err)
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
		log.Printf("reconciliation failed: %v", err)
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
			log.Printf("job %s orphan on node %s, marking LOST", jobID, nodeID)
			s.markJobLost(jobID, "Process disappeared from agent")
		}
		delete(agentJobIDs, jobID)
	}

	for jobID := range agentJobIDs {
		log.Printf("job %s zombie on node %s, killing", jobID, nodeID)
		s.killZombie(addr, jobID)
	}
}

func (s *Server) markJobLost(jobID string, reason string) {
	tx, err := s.db.Begin()
	if err != nil {
		log.Printf("Error starting transaction for markJobLost %s: %v", jobID, err)
		return
	}
	defer tx.Rollback()

	now := time.Now().UTC()
	nowStr := formatTime(now)
	_, err = tx.Exec("UPDATE jobs SET state = ?, finished_at = ?, reason = ? WHERE id = ?", models.JobStateLost, nowStr, reason, jobID)
	if err != nil {
		log.Printf("Error updating job %s to LOST: %v", jobID, err)
	}
	_, err = tx.Exec(`
		DELETE FROM gpu_leases
		WHERE allocation_id IN (SELECT id FROM allocations WHERE job_id = ?)
	`, jobID)
	if err != nil {
		log.Printf("Error clearing leases for job %s (LOST): %v", jobID, err)
	}
	_, err = tx.Exec("UPDATE allocations SET status = 'LOST', released_at = ? WHERE job_id = ? AND released_at IS NULL", nowStr, jobID)
	if err != nil {
		log.Printf("Error updating allocation for job %s to LOST: %v", jobID, err)
	}

	if err := tx.Commit(); err != nil {
		log.Printf("Error committing markJobLost for %s: %v", jobID, err)
	}
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
	NodeID        string       `json:"node_id"`
	AgentVersion  string       `json:"agent_version"`
	Addr          string       `json:"addr"`
	GPUs          []models.GPU `json:"gpus"`
	ActiveJobIDs  []string     `json:"active_job_ids"`
	MemoryTotalMB int          `json:"memory_total_mb"`
	MemoryUsedMB  int          `json:"memory_used_mb"`
}

func (s *Server) handleHeartbeat(w http.ResponseWriter, r *http.Request) {
	var req HeartbeatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}

	nowStr := formatTime(time.Now())

	tx, err := s.db.Begin()
	if err != nil {
		http.Error(w, "db error begin", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()

	var existingStatus string
	err = s.db.QueryRow("SELECT status FROM nodes WHERE id = ?", req.NodeID).Scan(&existingStatus)
	isNew := err != nil

	// Update node
	_, err = tx.Exec(`
		INSERT INTO nodes (id, status, last_heartbeat_at, agent_version, addr, memory_total_mb, memory_used_mb)
		VALUES (?, 'UP', ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			status = 'UP',
			last_heartbeat_at = excluded.last_heartbeat_at,
			agent_version = excluded.agent_version,
			addr = excluded.addr,
			memory_total_mb = excluded.memory_total_mb,
			memory_used_mb = excluded.memory_used_mb
	`, req.NodeID, nowStr, req.AgentVersion, req.Addr, req.MemoryTotalMB, req.MemoryUsedMB)
	if err != nil {
		log.Printf("heartbeat failed for node %s: %v", req.NodeID, err)
		http.Error(w, "db error node update", http.StatusInternalServerError)
		return
	}

	if isNew {
		s.events.Emit(events.TypeNodeRegistered, nil, &req.NodeID, map[string]string{"addr": req.Addr, "version": req.AgentVersion})
	} else if existingStatus == "DOWN" {
		s.events.Emit(events.TypeNodeRecovered, nil, &req.NodeID, nil)
	}

	// Update GPUs
	for _, gpu := range req.GPUs {
		_, err := tx.Exec(`
			INSERT INTO gpus (id, node_id, idx, uuid, name, memory_mb, health, utilization, memory_used_mb, last_seen_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET
				idx = excluded.idx,
				uuid = excluded.uuid,
				name = excluded.name,
				memory_mb = excluded.memory_mb,
				health = excluded.health,
				utilization = excluded.utilization,
				memory_used_mb = excluded.memory_used_mb,
				last_seen_at = excluded.last_seen_at
		`, fmt.Sprintf("%s-%s", req.NodeID, gpu.UUID), req.NodeID, gpu.Idx, gpu.UUID, gpu.Name, gpu.MemoryMB, gpu.Health, gpu.Utilization, gpu.MemoryUsedMB, nowStr)
		if err != nil {
			log.Printf("Heartbeat: error updating GPU %s for node %s: %v", gpu.UUID, req.NodeID, err)
			http.Error(w, "db error gpu update", http.StatusInternalServerError)
			return
		}
	}

	if err := tx.Commit(); err != nil {
		http.Error(w, "db error commit", http.StatusInternalServerError)
		return
	}

	// Active Job Reconciliation
	// 1. Get expected jobs (RUNNING/STARTING) for this node from DB
	dbRows, err := s.db.Query(`
		SELECT j.id FROM jobs j
		JOIN allocations a ON a.job_id = j.id
		WHERE a.node_id = ? AND a.released_at IS NULL AND j.state IN (?, ?)
	`, req.NodeID, models.JobStateRunning, models.JobStateStarting)

	if err == nil {
		expectedJobs := make(map[string]bool)
		for dbRows.Next() {
			var bid string
			dbRows.Scan(&bid)
			expectedJobs[bid] = true
		}
		dbRows.Close()

		reportedJobs := make(map[string]bool)
		for _, id := range req.ActiveJobIDs {
			reportedJobs[id] = true
		}

		// Detect orphans: DB expects job but agent doesn't report it
		for id := range expectedJobs {
			if !reportedJobs[id] {
				log.Printf("Heartbeat: job %s missing from node %s (Orphan), marking LOST", id, req.NodeID)
				s.markJobLost(id, "Process missing in heartbeat")
			}
		}

		// Detect zombies: agent reports job but DB doesn't expect it
		for id := range reportedJobs {
			if !expectedJobs[id] {
				log.Printf("Heartbeat: job %s is zombie on node %s, killing", id, req.NodeID)
				go s.killZombie(req.Addr, id)
			}
		}
	} else {
		log.Printf("Heartbeat: error querying expected jobs: %v", err)
	}

	w.WriteHeader(http.StatusOK)
}

type JobSubmitRequest struct {
	GPUCount          int               `json:"gpu_count"`
	Priority          int               `json:"priority"`
	Command           string            `json:"command"`
	CWD               string            `json:"cwd"`
	Env               map[string]string `json:"env"`
	MaxRuntimeMinutes int               `json:"max_runtime_minutes"`
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

	// Validate GPU count against the largest node in the cluster
	var maxGPUs int
	if err := s.db.QueryRow("SELECT COUNT(*) as c FROM gpus GROUP BY node_id ORDER BY c DESC LIMIT 1").Scan(&maxGPUs); err == nil && maxGPUs > 0 && req.GPUCount > maxGPUs {
		http.Error(w, fmt.Sprintf("invalid request: job requests %d GPUs but largest node only has %d", req.GPUCount, maxGPUs), http.StatusBadRequest)
		return
	}

	envJSON, _ := json.Marshal(req.Env)
	jobID := uuid.New().String()
	nowStr := formatTime(time.Now())

	if _, err := s.db.Exec(`
		INSERT INTO jobs (id, owner_id, state, priority, gpu_count, command, cwd, env_json, max_runtime_minutes, created_at, queued_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, jobID, user.ID, models.JobStateQueued, req.Priority, req.GPUCount, req.Command, req.CWD, string(envJSON), req.MaxRuntimeMinutes, nowStr, nowStr); err != nil {
		http.Error(w, "db error job insert", http.StatusInternalServerError)
		return
	}

	s.events.Emit(events.TypeJobSubmitted, &jobID, nil, map[string]any{
		"owner_id":  user.ID,
		"gpu_count": req.GPUCount,
		"priority":  req.Priority,
	})

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

	var jobs = []models.Job{}
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
	nowStr := formatTime(time.Now())
	rows, err := s.db.Query(`
		SELECT n.id, n.status, n.last_heartbeat_at, n.agent_version, n.addr,
		(SELECT COUNT(*) FROM gpus g WHERE g.node_id = n.id AND g.health = 'OK') as gpu_count,
		(SELECT COUNT(*) FROM gpus g WHERE g.node_id = n.id AND g.health = 'OK' AND g.id NOT IN (SELECT gpu_id FROM gpu_leases l WHERE l.expires_at > ?)) as gpu_free,
		COALESCE((SELECT CAST(AVG(utilization) AS INTEGER) FROM gpus g WHERE g.node_id = n.id AND g.health = 'OK'), 0) as gpu_utilization,
		n.memory_total_mb, n.memory_used_mb
		FROM nodes n
	`, nowStr)
	if err != nil {
		log.Printf("handleNodeList error: %v", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var nodes = []models.Node{}
	for rows.Next() {
		var n models.Node
		if err := rows.Scan(&n.ID, &n.Status, &n.LastHeartbeatAt, &n.AgentVersion, &n.Addr, &n.GPUCount, &n.GPUFree, &n.GPUUtilization, &n.MemoryTotalMB, &n.MemoryUsedMB); err != nil {
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

	nowStr := formatTime(time.Now())

	// 1. Update Job
	query := "UPDATE jobs SET state = ?"
	args := []any{req.State}

	if req.State == models.JobStateStarting || req.State == models.JobStateRunning {
		query += ", started_at = COALESCE(started_at, ?)"
		args = append(args, nowStr)
	}
	if req.State == models.JobStateSucceeded || req.State == models.JobStateFailed || req.State == models.JobStateCanceled {
		query += ", finished_at = ?, exit_code = ?"
		args = append(args, nowStr, req.ExitCode)
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

		_, err = tx.Exec("UPDATE allocations SET status = ?, released_at = ? WHERE job_id = ?", string(req.State), nowStr, jobID)
		if err != nil {
			http.Error(w, "db error allocation update", http.StatusInternalServerError)
			return
		}
	}

	if err := tx.Commit(); err != nil {
		http.Error(w, "db error commit", http.StatusInternalServerError)
		return
	}

	s.events.Emit(events.TypeJobCanceled, &jobID, nil, map[string]string{"reason": "user_request"})

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
	if follow == "true" {
		flusher, ok := w.(http.Flusher)
		if !ok {
			// Fallback if flusher not supported
			_, _ = io.Copy(w, resp.Body)
			return
		}

		buf := make([]byte, 4096)
		for {
			n, err := resp.Body.Read(buf)
			if n > 0 {
				_, _ = w.Write(buf[:n])
				flusher.Flush()
			}
			if err != nil {
				break
			}
		}
	} else {
		_, _ = io.Copy(w, resp.Body)
	}
}

func (s *Server) handleJobEvents(w http.ResponseWriter, r *http.Request) {
	jobID := r.PathValue("id")
	rows, err := s.db.Query("SELECT id, at, type, job_id, node_id, payload_json FROM events WHERE job_id = ? ORDER BY at ASC", jobID)
	if err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var events []models.Event
	for rows.Next() {
		var e models.Event
		if err := rows.Scan(&e.ID, &e.At, &e.Type, &e.JobID, &e.NodeID, &e.PayloadJSON); err != nil {
			http.Error(w, "db error scan", http.StatusInternalServerError)
			return
		}
		events = append(events, e)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(events)
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
		// Fallback: mark canceled if allocation missing
		_, _ = s.db.Exec("UPDATE jobs SET state = ? WHERE id = ?", models.JobStateCanceled, jobID)
		w.WriteHeader(http.StatusOK)
		return
	}

	// Mark job canceled and release resources
	log.Printf("cancel job %s (owner: %s, state: %s)", jobID, user.ID, state)
	s.markJobCanceled(jobID)

	if addr != "" {
		// Notify agent to terminate job
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

	now := time.Now().UTC()
	nowStr := formatTime(now)
	// Overwrite any non-terminal state
	_, err = tx.Exec("UPDATE jobs SET state = ?, finished_at = ? WHERE id = ?", models.JobStateCanceled, nowStr, jobID)
	if err != nil {
		log.Printf("Error updating job %s to CANCELED: %v", jobID, err)
		return
	}

	_, err = tx.Exec(`
		DELETE FROM gpu_leases
		WHERE allocation_id IN (SELECT id FROM allocations WHERE job_id = ?)
	`, jobID)
	if err != nil {
		log.Printf("Error clearing leases for job %s (CANCELED): %v", jobID, err)
	}
	_, err = tx.Exec("UPDATE allocations SET status = ?, released_at = ? WHERE job_id = ? AND released_at IS NULL", string(models.JobStateCanceled), nowStr, jobID)
	if err != nil {
		log.Printf("Error updating allocation for job %s to CANCELED: %v", jobID, err)
	}

	if err := tx.Commit(); err != nil {
		log.Printf("Error committing cancel for %s: %v", jobID, err)
	}
}

func (s *Server) handleWhoami(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	if user == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(user)
}
