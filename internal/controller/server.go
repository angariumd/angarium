package controller

import (
	"encoding/json"
	"fmt"
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

	// Agent routes (shared token auth)
	mux.HandleFunc("POST /v1/agent/heartbeat", s.handleHeartbeat)

	// CLI routes (user token auth)
	cliHandler := http.NewServeMux()
	cliHandler.HandleFunc("POST /v1/jobs", s.handleJobSubmit)
	cliHandler.HandleFunc("GET /v1/jobs", s.handleJobList)
	cliHandler.HandleFunc("GET /v1/nodes", s.handleNodeList)

	mux.Handle("/v1/jobs", s.auth.Middleware(cliHandler))
	mux.Handle("/v1/nodes", s.auth.Middleware(cliHandler))

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
	// TODO: Shared token auth for agents
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
	rows, err := s.db.Query("SELECT id, owner_id, state, priority, gpu_count, command, cwd, created_at FROM jobs ORDER BY created_at DESC")
	if err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var jobs []models.Job
	for rows.Next() {
		var j models.Job
		if err := rows.Scan(&j.ID, &j.OwnerID, &j.State, &j.Priority, &j.GPUCount, &j.Command, &j.CWD, &j.CreatedAt); err != nil {
			http.Error(w, "db error scan", http.StatusInternalServerError)
			return
		}
		jobs = append(jobs, j)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(jobs)
}

func (s *Server) handleNodeList(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.Query("SELECT id, status, last_heartbeat_at, agent_version, addr FROM nodes")
	if err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var nodes []models.Node
	for rows.Next() {
		var n models.Node
		if err := rows.Scan(&n.ID, &n.Status, &n.LastHeartbeatAt, &n.AgentVersion, &n.Addr); err != nil {
			http.Error(w, "db error scan", http.StatusInternalServerError)
			return
		}
		nodes = append(nodes, n)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(nodes)
}
