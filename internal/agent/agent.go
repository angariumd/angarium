package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/angariumd/angarium/internal/models"
)

type Agent struct {
	nodeID        string
	controllerURL string
	gpuProvider   GPUProvider
	version       string
	addr          string
	logDir        string
	sharedToken   string
	mu            sync.Mutex
	activeJobs    map[string]*JobRunner
}

func NewAgent(nodeID, controllerURL string, gpuProvider GPUProvider, version, addr, sharedToken string) *Agent {
	a := &Agent{
		nodeID:        nodeID,
		controllerURL: controllerURL,
		gpuProvider:   gpuProvider,
		version:       version,
		addr:          addr,
		logDir:        "/tmp/angarium/jobs",
		sharedToken:   sharedToken,
		activeJobs:    make(map[string]*JobRunner),
	}

	// Create log dir if not exists
	os.MkdirAll(a.logDir, 0755)

	// Load recovered jobs
	a.loadState()

	return a
}

func (a *Agent) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/agent/launch", a.handleLaunch)
	mux.HandleFunc("POST /v1/agent/terminate", a.handleTerminate)
	mux.HandleFunc("GET /v1/agent/jobs/{id}/logs", a.handleLogs)
	mux.HandleFunc("GET /v1/agent/running", a.handleRunning)
	return mux
}

type LaunchRequest struct {
	Job      models.Job `json:"job"`
	GPUUUIDs []string   `json:"gpu_uuids"`
}

func (a *Agent) handleLaunch(w http.ResponseWriter, r *http.Request) {
	var req LaunchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}

	runner := NewJobRunner(req.Job, req.GPUUUIDs, a.logDir)
	if err := runner.Start(); err != nil {
		http.Error(w, fmt.Sprintf("launch failed: %v", err), http.StatusInternalServerError)
		return
	}

	a.mu.Lock()
	a.activeJobs[req.Job.ID] = runner
	a.saveStateLocked()
	a.mu.Unlock()

	// Wait for process to start properly (could be immediate)
	go func() {
		// Report STARTING first to acknowledge
		a.updateJobStatus(req.Job.ID, models.JobStateStarting, nil)

		// Report RUNNING state
		a.updateJobStatus(req.Job.ID, models.JobStateRunning, nil)

		err := runner.Wait()

		state := models.JobStateSucceeded
		var code int
		if err != nil {
			state = models.JobStateFailed
			if exitErr, ok := err.(*exec.ExitError); ok {
				code = exitErr.ExitCode()
			} else {
				code = -1
			}
		} else {
			code = 0
		}
		exitCode := &code

		a.mu.Lock()
		delete(a.activeJobs, req.Job.ID)
		a.saveStateLocked()
		a.mu.Unlock()
		a.updateJobStatus(req.Job.ID, state, exitCode)
	}()

	w.WriteHeader(http.StatusAccepted)
}

func (a *Agent) handleTerminate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		JobID string `json:"job_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}

	a.mu.Lock()
	runner, ok := a.activeJobs[req.JobID]
	a.mu.Unlock()

	if !ok {
		http.Error(w, "job not found", http.StatusNotFound)
		return
	}

	go func() {
		runner.Stop()
		a.updateJobStatus(req.JobID, models.JobStateCanceled, nil)
	}()

	w.WriteHeader(http.StatusAccepted)
}

type RunningJob struct {
	JobID string `json:"job_id"`
	PID   int    `json:"pid"`
}

func (a *Agent) handleRunning(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	running := make([]RunningJob, 0, len(a.activeJobs))
	for id, runner := range a.activeJobs {
		running = append(running, RunningJob{
			JobID: id,
			PID:   runner.PID(),
		})
	}
	a.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(running)
}

func (a *Agent) updateJobStatus(jobID string, state models.JobState, exitCode *int) {
	url := fmt.Sprintf("%s/v1/agent/jobs/%s/state", a.controllerURL, jobID)
	reqObj := struct {
		State    models.JobState `json:"state"`
		ExitCode *int            `json:"exit_code,omitempty"`
	}{
		State:    state,
		ExitCode: exitCode,
	}

	body, _ := json.Marshal(reqObj)
	req, _ := http.NewRequest("POST", url, bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Agent-Token", a.sharedToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Printf("Error updating job status: %v\n", err)
		return
	}
	defer resp.Body.Close()
}
func (a *Agent) handleLogs(w http.ResponseWriter, r *http.Request) {
	jobID := r.PathValue("id")
	follow := r.URL.Query().Get("follow") == "true"

	logPath := filepath.Join(a.logDir, fmt.Sprintf("%s.log", jobID))
	f, err := os.Open(logPath)
	if err != nil {
		http.Error(w, "log not found", http.StatusNotFound)
		return
	}
	defer f.Close()

	w.Header().Set("Content-Type", "text/plain")
	w.Header().Set("Transfer-Encoding", "chunked")

	// Read everything current
	_, _ = io.Copy(w, f)
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}

	if !follow {
		return
	}

	// Stream new data as it arrives
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			// Just copy any new data
			_, _ = io.Copy(w, f)
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}

			// If job is no longer active, we should eventually stop
			a.mu.Lock()
			_, active := a.activeJobs[jobID]
			a.mu.Unlock()

			if !active {
				// Final copy
				_, _ = io.Copy(w, f)
				return
			}
		}
	}
}

func (a *Agent) StartHeartbeat(interval time.Duration) {
	ticker := time.NewTicker(interval)
	// Initial heartbeat
	a.sendHeartbeat()

	go func() {
		for range ticker.C {
			a.sendHeartbeat()
		}
	}()
}

type HeartbeatRequest struct {
	NodeID       string       `json:"node_id"`
	AgentVersion string       `json:"agent_version"`
	Addr         string       `json:"addr"`
	GPUs         []models.GPU `json:"gpus"`
	ActiveJobIDs []string     `json:"active_job_ids"`
}

func (a *Agent) sendHeartbeat() {
	gpus, err := a.gpuProvider.GetGPUs()
	if err != nil {
		fmt.Printf("Error getting GPUs: %v\n", err)
		return
	}

	a.mu.Lock()
	activeIDs := make([]string, 0, len(a.activeJobs))
	for id := range a.activeJobs {
		activeIDs = append(activeIDs, id)
	}
	a.mu.Unlock()

	req := HeartbeatRequest{
		NodeID:       a.nodeID,
		AgentVersion: a.version,
		Addr:         a.addr,
		GPUs:         gpus,
		ActiveJobIDs: activeIDs,
	}

	body, _ := json.Marshal(req)
	url := fmt.Sprintf("%s/v1/agent/heartbeat", a.controllerURL)

	reqHeader, _ := http.NewRequest("POST", url, bytes.NewBuffer(body))
	reqHeader.Header.Set("Content-Type", "application/json")
	reqHeader.Header.Set("X-Agent-Token", a.sharedToken)

	resp, err := http.DefaultClient.Do(reqHeader)
	if err != nil {
		fmt.Printf("Error sending heartbeat: %v\n", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		fmt.Printf("Heartbeat failed with status: %d\n", resp.StatusCode)
	}
}

// Persistence logic

type PersistentState struct {
	ActiveJobs []struct {
		JobID string `json:"job_id"`
		PID   int    `json:"pid"`
	} `json:"active_jobs"`
}

func (a *Agent) statePath() string {
	return filepath.Join(a.logDir, "agent_state.json")
}

func (a *Agent) saveState() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.saveStateLocked()
}

func (a *Agent) saveStateLocked() {
	state := PersistentState{}
	for id, runner := range a.activeJobs {
		pid := runner.PID()
		if pid > 0 {
			state.ActiveJobs = append(state.ActiveJobs, struct {
				JobID string `json:"job_id"`
				PID   int    `json:"pid"`
			}{JobID: id, PID: pid})
		}
	}

	data, _ := json.MarshalIndent(state, "", "  ")
	_ = os.WriteFile(a.statePath(), data, 0644)
}

func (a *Agent) loadState() {
	data, err := os.ReadFile(a.statePath())
	if err != nil {
		return // No state to load or error reading
	}

	var state PersistentState
	if err := json.Unmarshal(data, &state); err != nil {
		fmt.Printf("Error unmarshaling state: %v\n", err)
		return
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	for _, j := range state.ActiveJobs {
		// Check if process is alive
		// os.FindProcess always succeeds on Unix, verify with signal 0
		proc, err := os.FindProcess(j.PID)
		if err == nil {
			if err := proc.Signal(syscall.Signal(0)); err == nil {
				// Process is alive, re-adopt it
				fmt.Printf("Recovering job %s (PID %d)\n", j.JobID, j.PID)
				a.activeJobs[j.JobID] = NewRecoveredJobRunner(models.Job{ID: j.JobID}, j.PID)
			}
		}
	}
}
