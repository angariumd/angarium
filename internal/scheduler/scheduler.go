package scheduler

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/angariumd/angarium/internal/db"
	"github.com/angariumd/angarium/internal/models"
	"github.com/google/uuid"
)

type Scheduler struct {
	db *db.DB
}

func New(db *db.DB) *Scheduler {
	return &Scheduler{db: db}
}

func (s *Scheduler) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.Schedule(ctx)
			s.CleanupLeases(ctx)
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
}

func (s *Scheduler) tryScheduleJob(job models.Job) error {
	nodes, err := s.getAvailableNodesAndGPUs()
	if err != nil {
		return err
	}

	if len(nodes) == 0 {
		return s.updateJobReason(job.ID, "No UP nodes found")
	}

	var bestNode *nodeCapacity
	for i := range nodes {
		if len(nodes[i].available) >= job.GPUCount {
			if bestNode == nil || len(nodes[i].available) < len(bestNode.available) {
				bestNode = &nodes[i]
			}
		}
	}

	if bestNode == nil {
		// Identify why it failed
		totalAvailable := 0
		maxSingleNode := 0
		for _, n := range nodes {
			totalAvailable += len(n.available)
			if len(n.available) > maxSingleNode {
				maxSingleNode = len(n.available)
			}
		}

		reason := fmt.Sprintf("Insufficient GPUs on a single node (requested %d, max available %d)", job.GPUCount, maxSingleNode)
		if totalAvailable < job.GPUCount {
			reason = fmt.Sprintf("Insufficient total GPUs in cluster (requested %d, total available %d)", job.GPUCount, totalAvailable)
		}
		return s.updateJobReason(job.ID, reason)
	}

	// Found a node! Select the first N GPUs (best-fit packing already decided the node)
	selectedGPUs := bestNode.available[:job.GPUCount]
	return s.allocateJob(job, bestNode.nodeID, selectedGPUs)
}

func (s *Scheduler) getAvailableNodesAndGPUs() ([]nodeCapacity, error) {
	// 1. Get all UP nodes
	rows, err := s.db.Query("SELECT id FROM nodes WHERE status = 'UP'")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	nodeIDs := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		nodeIDs = append(nodeIDs, id)
	}

	if len(nodeIDs) == 0 {
		return nil, nil
	}

	// 2. Get all GPUs for these nodes that are NOT currently leased/allocated
	// Note: We check gpu_leases for active leases.
	// Since we don't have a release yet, we assume leases exist until they expire or are manually removed.
	capacities := []nodeCapacity{}
	for _, nodeID := range nodeIDs {
		gpuRows, err := s.db.Query(`
			SELECT id, node_id, idx, uuid, name, memory_mb, health, last_seen_at
			FROM gpus
			WHERE node_id = ? AND health = 'OK'
			AND id NOT IN (SELECT gpu_id FROM gpu_leases WHERE expires_at > datetime('now'))
		`, nodeID)
		if err != nil {
			return nil, err
		}

		gpus := []models.GPU{}
		for gpuRows.Next() {
			var g models.GPU
			if err := gpuRows.Scan(&g.ID, &g.NodeID, &g.Idx, &g.UUID, &g.Name, &g.MemoryMB, &g.Health, &g.LastSeenAt); err != nil {
				gpuRows.Close()
				return nil, err
			}
			gpus = append(gpus, g)
		}
		gpuRows.Close()
		capacities = append(capacities, nodeCapacity{nodeID: nodeID, available: gpus})
	}

	return capacities, nil
}

func (s *Scheduler) allocateJob(job models.Job, nodeID string, gpus []models.GPU) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	allocationID := uuid.New().String()
	now := time.Now()
	expiresAt := now.Add(1 * time.Minute) // Initial lease for starting

	// 1. Update Job state
	_, err = tx.Exec("UPDATE jobs SET state = ?, reason = NULL WHERE id = ?", models.JobStateAllocated, job.ID)
	if err != nil {
		return err
	}

	// 2. Create Allocation
	_, err = tx.Exec(`
		INSERT INTO allocations (id, job_id, node_id, status, created_at)
		VALUES (?, ?, ?, ?, ?)
	`, allocationID, job.ID, nodeID, "ALLOCATED", now)
	if err != nil {
		return err
	}

	// 3. Create GPU Leases
	for _, gpu := range gpus {
		_, err = tx.Exec(`
			INSERT INTO gpu_leases (gpu_id, allocation_id, leased_at, expires_at)
			VALUES (?, ?, ?, ?)
		`, gpu.ID, allocationID, now, expiresAt)
		if err != nil {
			return err
		}
	}

	return tx.Commit()
}

func (s *Scheduler) updateJobReason(jobID string, reason string) error {
	_, err := s.db.Exec("UPDATE jobs SET reason = ? WHERE id = ?", reason, jobID)
	return err
}

func (s *Scheduler) CleanupLeases(ctx context.Context) {
	// Clean up leases that have expired
	// In the future, we should only clean up if the job hasn't moved beyond ALLOCATED/STARTING
	_, err := s.db.Exec("DELETE FROM gpu_leases WHERE expires_at < datetime('now')")
	if err != nil {
		log.Printf("Scheduler: error cleaning up leases: %v", err)
	}
}
