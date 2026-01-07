package models

import (
	"time"
)

type Node struct {
	ID              string    `json:"id"`
	Status          string    `json:"status"`
	LastHeartbeatAt time.Time `json:"last_heartbeat_at"`
	AgentVersion    string    `json:"agent_version"`
	Addr            string    `json:"addr"`
	GPUCount        int       `json:"gpu_count"`
	GPUFree         int       `json:"gpu_free"`
}

type GPU struct {
	ID           string    `json:"id"`
	NodeID       string    `json:"node_id"`
	Idx          int       `json:"idx"`
	UUID         string    `json:"uuid"`
	Name         string    `json:"name"`
	MemoryMB     int       `json:"memory_mb"`
	Health       string    `json:"health"`
	Utilization  int       `json:"utilization"` // 0-100
	MemoryUsedMB int       `json:"memory_used_mb"`
	LastSeenAt   time.Time `json:"last_seen_at"`
}

type User struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	TokenHash string `json:"-"`
}

type JobState string

const (
	JobStateQueued    JobState = "QUEUED"
	JobStateAllocated JobState = "ALLOCATED"
	JobStateStarting  JobState = "STARTING"
	JobStateRunning   JobState = "RUNNING"
	JobStateSucceeded JobState = "SUCCEEDED"
	JobStateFailed    JobState = "FAILED"
	JobStateCanceled  JobState = "CANCELED"
	JobStateLost      JobState = "LOST"
)

type Job struct {
	ID         string     `json:"id"`
	OwnerID    string     `json:"owner_id"`
	State      JobState   `json:"state"`
	Priority   int        `json:"priority"`
	GPUCount   int        `json:"gpu_count"`
	Command    string     `json:"command"`
	CWD        string     `json:"cwd"`
	EnvJSON    string     `json:"env_json"`
	CreatedAt  time.Time  `json:"created_at"`
	QueuedAt   time.Time  `json:"queued_at"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	ExitCode   *int       `json:"exit_code,omitempty"`
	Reason     *string    `json:"reason,omitempty"`
}

type Event struct {
	ID          int64     `json:"id"`
	At          time.Time `json:"at"`
	Type        string    `json:"type"`
	JobID       *string   `json:"job_id,omitempty"`
	NodeID      *string   `json:"node_id,omitempty"`
	PayloadJSON *string   `json:"payload_json,omitempty"`
}

type Allocation struct {
	ID         string     `json:"id"`
	JobID      string     `json:"job_id"`
	NodeID     string     `json:"node_id"`
	Status     string     `json:"status"`
	CreatedAt  time.Time  `json:"created_at"`
	ReleasedAt *time.Time `json:"released_at,omitempty"`
}

type GPULease struct {
	ID           int64     `json:"id"`
	GPUID        string    `json:"gpu_id"`
	AllocationID string    `json:"allocation_id"`
	LeasedAt     time.Time `json:"leased_at"`
	ExpiresAt    time.Time `json:"expires_at"`
}
