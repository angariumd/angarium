package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/angariumd/angarium/internal/models"
)

type JobRunner struct {
	job          models.Job
	gpuUUIDs     []string
	logDir       string
	cmd          *exec.Cmd
	monitoredPID int
	err          error
	finishedChan chan struct{}
}

func NewJobRunner(job models.Job, gpuUUIDs []string, logDir string) *JobRunner {
	return &JobRunner{
		job:          job,
		gpuUUIDs:     gpuUUIDs,
		logDir:       logDir,
		finishedChan: make(chan struct{}),
	}
}

func NewRecoveredJobRunner(job models.Job, pid int) *JobRunner {
	r := &JobRunner{
		job:          job,
		monitoredPID: pid,
		finishedChan: make(chan struct{}),
	}

	go func() {
		// Poll for process existence
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()

		for range ticker.C {
			if err := syscall.Kill(pid, 0); err != nil {
				// Process is gone
				close(r.finishedChan)
				return
			}
		}
	}()

	return r
}

// 50 MB limit
const MaxLogSize = 50 * 1024 * 1024

type LimitWriter struct {
	w       *os.File
	written int64
	limit   int64
}

func (l *LimitWriter) Write(p []byte) (n int, err error) {
	if l.written >= l.limit {
		return len(p), nil // Silently discard
	}
	if l.written+int64(len(p)) > l.limit {
		remaining := l.limit - l.written
		l.w.Write(p[:remaining])
		l.w.WriteString("\n[LOG LIMIT EXCEEDED - TRUNCATED]\n")
		l.written += int64(len(p))
		return len(p), nil
	}
	n, err = l.w.Write(p)
	l.written += int64(n)
	return n, err
}

func (r *JobRunner) Start() error {
	if err := os.MkdirAll(r.logDir, 0755); err != nil {
		return fmt.Errorf("creating log dir: %w", err)
	}
	logFile := filepath.Join(r.logDir, fmt.Sprintf("%s.log", r.job.ID))
	f, err := os.Create(logFile)
	if err != nil {
		return fmt.Errorf("creating log file: %w", err)
	}

	// Cap logs to prevent disk exhaustion
	limitWriter := &LimitWriter{w: f, limit: MaxLogSize}

	r.cmd = exec.Command("sh", "-c", r.job.Command)
	r.cmd.Dir = r.job.CWD
	r.cmd.Stdout = limitWriter
	r.cmd.Stderr = limitWriter

	// Environment variables
	var env []string
	if r.job.EnvJSON != "" {
		var envMap map[string]string
		if err := json.Unmarshal([]byte(r.job.EnvJSON), &envMap); err == nil {
			for k, v := range envMap {
				env = append(env, fmt.Sprintf("%s=%s", k, v))
			}
		}
	}

	// GPU isolation
	if len(r.gpuUUIDs) > 0 {
		visibleStr := ""
		for i, uuid := range r.gpuUUIDs {
			if i > 0 {
				visibleStr += ","
			}
			visibleStr += uuid
		}
		env = append(env, fmt.Sprintf("CUDA_VISIBLE_DEVICES=%s", visibleStr))
	}
	r.cmd.Env = append(os.Environ(), env...)

	// New process group for clean signal handling
	r.cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true,
	}

	if err := r.cmd.Start(); err != nil {
		f.Close()
		return fmt.Errorf("starting command: %w", err)
	}

	go func() {
		r.err = r.cmd.Wait()
		f.Close()
		close(r.finishedChan)
	}()

	return nil
}

func (r *JobRunner) Wait() error {
	<-r.finishedChan
	return r.err
}

func (r *JobRunner) Stop() error {
	if r.monitoredPID > 0 {
		// Recovered job
		syscall.Kill(r.monitoredPID, syscall.SIGTERM)
		// Wait grace period
		select {
		case <-r.finishedChan:
			return nil
		case <-time.After(5 * time.Second):
			syscall.Kill(r.monitoredPID, syscall.SIGKILL)
			return nil
		}
	}

	if r.cmd == nil || r.cmd.Process == nil {
		return nil
	}

	// Check if already finished
	select {
	case <-r.finishedChan:
		return nil
	default:
	}

	// Send SIGTERM to the process group
	pgid, err := syscall.Getpgid(r.cmd.Process.Pid)
	if err == nil {
		syscall.Kill(-pgid, syscall.SIGTERM)
	} else {
		r.cmd.Process.Signal(syscall.SIGTERM)
	}

	// Wait for grace period
	select {
	case <-r.finishedChan:
		return nil
	case <-time.After(5 * time.Second):
		// Escalate to SIGKILL
		if err == nil { // err here is from Getpgid, so if pgid was successfully retrieved
			syscall.Kill(-pgid, syscall.SIGKILL)
		} else {
			r.cmd.Process.Kill()
		}
		<-r.finishedChan // Wait for the process to actually finish after SIGKILL
		return nil       // Return nil as the job is now stopped, regardless of how.
	}
}

func (r *JobRunner) PID() int {
	if r.monitoredPID > 0 {
		return r.monitoredPID
	}
	if r.cmd != nil && r.cmd.Process != nil {
		return r.cmd.Process.Pid
	}
	return 0
}
