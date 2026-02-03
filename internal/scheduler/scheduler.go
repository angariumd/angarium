package scheduler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/angariumd/angarium/internal/db"
	"github.com/angariumd/angarium/internal/events"
	"github.com/angariumd/angarium/internal/models"
	"github.com/google/uuid"
)

const SQLTimeLayout = "2006-01-02 15:04:05"

func formatTime(t time.Time) string {
	return t.UTC().Format(SQLTimeLayout)
}

const DefaultLeaseDuration = 1 * time.Minute

type Scheduler struct {
	db         *db.DB
	events     *events.EventManager
	AgentToken string
}

func New(db *db.DB, events *events.EventManager, agentToken string) *Scheduler {
	return &Scheduler{
		db:         db,
		events:     events,
		AgentToken: agentToken,
	}
}

func (s *Scheduler) Run(ctx context.Context, interval time.Duration) {
	// Initial passes for recovery and pending jobs
	s.CleanupLeases(ctx)
	s.Schedule(ctx)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.Schedule(ctx)
			s.CleanupLeases(ctx)
			s.EnforceMaxRuntime(ctx)
		}
	}
}

func (s *Scheduler) Schedule(ctx context.Context) {
	jobs, err := s.getQueuedJobs()
	if err != nil {
		log.Printf("Scheduler: error getting queued jobs: %v", err)
		return
	}

	for _, job := range jobs {
		if err := s.tryScheduleJob(job); err != nil {
			log.Printf("Scheduler: error scheduling job %s: %v", job.ID, err)
		}
	}
}

func (s *Scheduler) getQueuedJobs() ([]models.Job, error) {
	rows, err := s.db.Query(`
		SELECT id, owner_id, state, priority, gpu_count, command, cwd, env_json, created_at, queued_at
		FROM jobs WHERE state = ? ORDER BY priority DESC, created_at ASC
	`, models.JobStateQueued)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var jobs []models.Job
	for rows.Next() {
		var j models.Job
		if err := rows.Scan(&j.ID, &j.OwnerID, &j.State, &j.Priority, &j.GPUCount, &j.Command, &j.CWD, &j.EnvJSON, &j.CreatedAt, &j.QueuedAt); err != nil {
			return nil, err
		}
		jobs = append(jobs, j)
	}
	return jobs, nil
}

type nodeCapacity struct {
	nodeID    string
	available []models.GPU
	healthy   []models.GPU
}

type clusterStats struct {
	totalHealthyGPUs       int
	maxHealthySingleNode   int
	totalAvailableGPUs     int
	maxAvailableSingleNode int
	nodes                  []nodeCapacity
}

func (s *Scheduler) tryScheduleJob(job models.Job) error {
	stats, err := s.getClusterStats()
	if err != nil {
		return err
	}

	if len(stats.nodes) == 0 {
		return s.updateJobReason(job.ID, "No nodes are currently UP")
	}

	// Check if the job is physically satisfyable by the cluster (hardware check)
	if stats.totalHealthyGPUs < job.GPUCount {
		return s.updateJobReason(job.ID, fmt.Sprintf("Cluster only has %d healthy GPUs total, but job requires %d", stats.totalHealthyGPUs, job.GPUCount))
	}
	if stats.maxHealthySingleNode < job.GPUCount {
		return s.updateJobReason(job.ID, fmt.Sprintf("No single node has %d GPUs (max capacity is %d)", job.GPUCount, stats.maxHealthySingleNode))
	}

	// Hardware exists, check if it's currently available (occupancy check)
	var bestNode *nodeCapacity
	for i := range stats.nodes {
		if len(stats.nodes[i].available) >= job.GPUCount {
			if bestNode == nil || len(stats.nodes[i].available) < len(bestNode.available) {
				bestNode = &stats.nodes[i]
			}
		}
	}

	if bestNode == nil {
		if stats.totalAvailableGPUs < job.GPUCount {
			busyCount := stats.totalHealthyGPUs - stats.totalAvailableGPUs
			return s.updateJobReason(job.ID, fmt.Sprintf("Waiting for GPUs to be released (%d GPUs currently busy/leased)", busyCount))
		}
		// If totalAvailable >= job.GPUCount but bestNode is still nil, it means fragmentation
		return s.updateJobReason(job.ID, fmt.Sprintf("Waiting for a single node to have %d free GPUs (fragmented: %d total free across nodes)", job.GPUCount, stats.totalAvailableGPUs))
	}

	// Found a node! Select the first N GPUs (best-fit packing already decided the node)
	selectedGPUs := bestNode.available[:job.GPUCount]
	return s.allocateJob(job, bestNode.nodeID, selectedGPUs)
}

func (s *Scheduler) getClusterStats() (*clusterStats, error) {
	// Get all UP nodes
	rows, err := s.db.Query("SELECT id FROM nodes WHERE status = 'UP'")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var nodeIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		nodeIDs = append(nodeIDs, id)
	}

	stats := &clusterStats{nodes: []nodeCapacity{}}
	if len(nodeIDs) == 0 {
		return stats, nil
	}

	now := time.Now().UTC()
	for _, nodeID := range nodeIDs {
		gpuRows, err := s.db.Query(`
			SELECT g.id, g.node_id, g.idx, g.uuid, g.name, g.memory_mb, g.health, g.last_seen_at,
			       (SELECT COUNT(*) FROM gpu_leases l WHERE l.gpu_id = g.id AND l.expires_at > ?) as is_leased
			FROM gpus g
			WHERE g.node_id = ? AND g.health = 'OK'
		`, formatTime(now), nodeID)
		if err != nil {
			return nil, err
		}

		nodeCap := nodeCapacity{nodeID: nodeID, available: []models.GPU{}, healthy: []models.GPU{}}
		for gpuRows.Next() {
			var g models.GPU
			var isLeased int
			if err := gpuRows.Scan(&g.ID, &g.NodeID, &g.Idx, &g.UUID, &g.Name, &g.MemoryMB, &g.Health, &g.LastSeenAt, &isLeased); err != nil {
				gpuRows.Close()
				return nil, err
			}
			nodeCap.healthy = append(nodeCap.healthy, g)
			if isLeased == 0 {
				nodeCap.available = append(nodeCap.available, g)
			}
		}
		gpuRows.Close()
		stats.totalHealthyGPUs += len(nodeCap.healthy)
		if len(nodeCap.healthy) > stats.maxHealthySingleNode {
			stats.maxHealthySingleNode = len(nodeCap.healthy)
		}
		stats.totalAvailableGPUs += len(nodeCap.available)
		if len(nodeCap.available) > stats.maxAvailableSingleNode {
			stats.maxAvailableSingleNode = len(nodeCap.available)
		}
		stats.nodes = append(stats.nodes, nodeCap)
	}

	return stats, nil
}

func (s *Scheduler) allocateJob(job models.Job, nodeID string, gpus []models.GPU) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	allocationID := uuid.New().String()
	now := time.Now().UTC()
	expiresAt := now.Add(DefaultLeaseDuration) // Initial lease for starting
	nowStr := formatTime(now)
	expiresAtStr := formatTime(expiresAt)

	// Update Job state
	_, err = tx.Exec("UPDATE jobs SET state = ?, reason = NULL WHERE id = ?", models.JobStateAllocated, job.ID)
	if err != nil {
		return err
	}

	// Create Allocation
	_, err = tx.Exec(`
		INSERT INTO allocations (id, job_id, node_id, status, created_at)
		VALUES (?, ?, ?, ?, ?)
	`, allocationID, job.ID, nodeID, "ALLOCATED", nowStr)
	if err != nil {
		return err
	}

	// Create GPU Leases
	for _, gpu := range gpus {
		_, err = tx.Exec(`
			INSERT INTO gpu_leases (gpu_id, allocation_id, leased_at, expires_at)
			VALUES (?, ?, ?, ?)
		`, gpu.ID, allocationID, nowStr, expiresAtStr)
		if err != nil {
			return err
		}
	}

	if err := tx.Commit(); err != nil {
		return err
	}

	// Notify Agent
	go s.notifyAgentLaunch(job, nodeID, gpus)

	s.events.Emit(events.TypeJobAllocated, &job.ID, &nodeID, map[string]any{
		"gpu_count": len(gpus),
	})

	return nil
}

func (s *Scheduler) notifyAgentLaunch(job models.Job, nodeID string, gpus []models.GPU) {
	// Fetch node address
	var addr string
	err := s.db.QueryRow("SELECT addr FROM nodes WHERE id = ?", nodeID).Scan(&addr)
	if err != nil {
		log.Printf("Scheduler: error fetching node addr for %s: %v", nodeID, err)
		return
	}

	if addr == "" {
		log.Printf("Scheduler: node %s has no address", nodeID)
		return
	}

	gpuUUIDs := make([]string, len(gpus))
	for i, g := range gpus {
		gpuUUIDs[i] = g.UUID
	}

	reqBody := struct {
		Job      models.Job `json:"job"`
		GPUUUIDs []string   `json:"gpu_uuids"`
	}{
		Job:      job,
		GPUUUIDs: gpuUUIDs,
	}

	body, _ := json.Marshal(reqBody)
	url := fmt.Sprintf("%s/v1/agent/launch", addr)

	// Send launch command to the target node
	req, _ := http.NewRequest("POST", url, bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Agent-Token", s.AgentToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("Scheduler: failed to notify agent %s at %s: %v", nodeID, addr, err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		log.Printf("Scheduler: agent %s at %s rejected launch: %d", nodeID, addr, resp.StatusCode)
	}
}

func (s *Scheduler) updateJobReason(jobID string, reason string) error {
	_, err := s.db.Exec("UPDATE jobs SET reason = ? WHERE id = ?", reason, jobID)
	return err
}

func (s *Scheduler) CleanupLeases(ctx context.Context) {
	nowStr := formatTime(time.Now())
	// Identify jobs that are stuck in ALLOCATED/STARTING with expired leases
	rows, err := s.db.Query(`
		SELECT j.id, j.retry_count 
		FROM jobs j
		JOIN allocations a ON a.job_id = j.id
		JOIN gpu_leases l ON l.allocation_id = a.id
		WHERE l.expires_at < ?
		AND j.state IN (?, ?)
		GROUP BY j.id
	`, nowStr, models.JobStateAllocated, models.JobStateStarting)
	if err != nil {
		log.Printf("Scheduler: error querying stuck jobs: %v", err)
		return
	}
	defer rows.Close()

	var jobsToReap []struct {
		ID         string
		RetryCount int
	}

	for rows.Next() {
		var j struct {
			ID         string
			RetryCount int
		}
		if err := rows.Scan(&j.ID, &j.RetryCount); err == nil {
			jobsToReap = append(jobsToReap, j)
		}
	}
	rows.Close()

	// Process stuck jobs
	for _, j := range jobsToReap {
		if j.RetryCount < 3 {
			log.Printf("Scheduler: job %s stalled during allocation. Retrying (%d/3)...", j.ID, j.RetryCount+1)
			s.db.Exec(`
				UPDATE jobs 
				SET state = ?, retry_count = retry_count + 1, reason = 'Allocation timeout, retrying', queued_at = ?, started_at = NULL
				WHERE id = ?
			`, models.JobStateQueued, nowStr, j.ID)
			s.events.Emit(events.TypeLeaseExpired, &j.ID, nil, map[string]int{"retry_count": j.RetryCount + 1})
		} else {
			log.Printf("Scheduler: job %s stalled too many times. Marking FAILED.", j.ID)
			s.db.Exec(`
				UPDATE jobs 
				SET state = ?, finished_at = ?, reason = 'Allocation timeout: max retries exceeded'
				WHERE id = ?
			`, models.JobStateFailed, nowStr, j.ID)
			s.events.Emit(events.TypeJobLost, &j.ID, nil, map[string]string{"reason": "max_retries_exceeded"})
		}

		// Mark allocation as released (orphaned)
		s.db.Exec(`
			UPDATE allocations 
			SET status = 'RELEASED', released_at = ? 
			WHERE job_id = ? AND released_at IS NULL
		`, nowStr, j.ID)
	}

	// Clean up leases that have expired (this cleans up the physical reservation)
	_, err = s.db.Exec("DELETE FROM gpu_leases WHERE expires_at < ?", nowStr)
	if err != nil {
		log.Printf("Scheduler: error cleaning up leases: %v", err)
	}
}

func (s *Scheduler) EnforceMaxRuntime(ctx context.Context) {
	// Look for jobs in STARTING or RUNNING state that exceeded runtime
	// We use datetime() in SQL now that we have standardized formats
	nowStr := formatTime(time.Now())
	rows, err := s.db.Query(`
		SELECT id FROM jobs 
		WHERE state IN (?, ?) 
		AND max_runtime_minutes > 0 
		AND started_at IS NOT NULL
		AND datetime(started_at, '+' || max_runtime_minutes || ' minutes') < datetime(?)
	`, models.JobStateRunning, models.JobStateStarting, nowStr)
	if err != nil {
		log.Printf("Scheduler: error checking max runtime: %v", err)
		return
	}
	defer rows.Close()

	var expiredIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err == nil {
			expiredIDs = append(expiredIDs, id)
		}
	}
	rows.Close()

	for _, id := range expiredIDs {
		log.Printf("Scheduler: job %s exceeded max runtime. Canceling.", id)
		s.db.Exec(`
			UPDATE jobs 
			SET state = ?, finished_at = ?, reason = 'Max runtime exceeded' 
			WHERE id = ?
		`, models.JobStateCanceled, nowStr, id)
		s.events.Emit(events.TypeMaxRuntimeExcd, &id, nil, nil)

		s.db.Exec(`
			UPDATE allocations 
			SET status = 'CANCELED', released_at = ? 
			WHERE job_id = ? AND released_at IS NULL
		`, nowStr, id)
	}
}
