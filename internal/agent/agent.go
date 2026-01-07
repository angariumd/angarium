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
	activeJobs    map[string]*JobRunner
}

func NewAgent(nodeID, controllerURL string, gpuProvider GPUProvider, version, addr string) *Agent {
	return &Agent{
		nodeID:        nodeID,
		controllerURL: controllerURL,
		gpuProvider:   gpuProvider,
		version:       version,
		addr:          addr,
		logDir:        "/tmp/angarium/jobs",
		activeJobs:    make(map[string]*JobRunner),
	}
}

func (a *Agent) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/agent/launch", a.handleLaunch)
	mux.HandleFunc("POST /v1/agent/terminate", a.handleTerminate)
	mux.HandleFunc("GET /v1/agent/jobs/{id}/logs", a.handleLogs)
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

	a.activeJobs[req.Job.ID] = runner

	// Report STARTING state to controller
	go a.updateJobStatus(req.Job.ID, models.JobStateStarting, nil)

	// Wait for process to start properly (could be immediate)
	go func() {
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

		delete(a.activeJobs, req.Job.ID)
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

	runner, ok := a.activeJobs[req.JobID]
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
			if _, active := a.activeJobs[jobID]; !active {
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
}

func (a *Agent) sendHeartbeat() {
	gpus, err := a.gpuProvider.GetGPUs()
	if err != nil {
		fmt.Printf("Error getting GPUs: %v\n", err)
		return
	}

	req := HeartbeatRequest{
		NodeID:       a.nodeID,
		AgentVersion: a.version,
		Addr:         a.addr,
		GPUs:         gpus,
	}

	body, _ := json.Marshal(req)
	url := fmt.Sprintf("%s/v1/agent/heartbeat", a.controllerURL)

	resp, err := http.Post(url, "application/json", bytes.NewBuffer(body))
	if err != nil {
		fmt.Printf("Error sending heartbeat: %v\n", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		fmt.Printf("Heartbeat failed with status: %d\n", resp.StatusCode)
	}
}
