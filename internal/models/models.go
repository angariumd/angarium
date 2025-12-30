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
}

type GPU struct {
	ID         string    `json:"id"`
	NodeID     string    `json:"node_id"`
	Idx        int       `json:"idx"`
	UUID       string    `json:"uuid"`
	Name       string    `json:"name"`
	MemoryMB   int       `json:"memory_mb"`
	Health     string    `json:"health"`
	LastSeenAt time.Time `json:"last_seen_at"`
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
	ID        string    `json:"id"`
	OwnerID   string    `json:"owner_id"`
	State     JobState  `json:"state"`
	Priority  int       `json:"priority"`
	GPUCount  int       `json:"gpu_count"`
	Command   string    `json:"command"`
	CWD       string    `json:"cwd"`
	EnvJSON   string    `json:"env_json"`
	CreatedAt time.Time `json:"created_at"`
	QueuedAt  time.Time `json:"queued_at"`
	Reason    *string   `json:"reason,omitempty"`
}

type Event struct {
	ID          int64     `json:"id"`
	At          time.Time `json:"at"`
	Type        string    `json:"type"`
	JobID       *string   `json:"job_id,omitempty"`
	NodeID      *string   `json:"node_id,omitempty"`
	PayloadJSON *string   `json:"payload_json,omitempty"`
}
