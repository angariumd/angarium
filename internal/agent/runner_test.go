package agent

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/angariumd/angarium/internal/models"
)

func TestJobRunner_Start(t *testing.T) {
	logDir := t.TempDir()
	job := models.Job{
		ID:      "test-job-1",
		Command: "env && echo 'running'",
		CWD:     logDir,
		EnvJSON: `{"MY_VAR":"cool"}`,
	}
	gpuUUIDs := []string{"GPU-UUID-1", "GPU-UUID-2"}

	runner := NewJobRunner(job, gpuUUIDs, logDir)
	if err := runner.Start(); err != nil {
		t.Fatalf("failed to start: %v", err)
	}

	err := runner.Wait()
	if err != nil {
		t.Errorf("job failed: %v", err)
	}

	// Verify log file exists and contains output
	logFile := filepath.Join(logDir, "test-job-1.log")
	content, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("failed to read log: %v", err)
	}

	sContent := string(content)
	if !strings.Contains(sContent, "running") {
		t.Errorf("log missing expected output")
	}
	if !strings.Contains(sContent, "MY_VAR=cool") {
		t.Errorf("log missing environment variable output")
	}
	if !strings.Contains(sContent, "CUDA_VISIBLE_DEVICES=GPU-UUID-1,GPU-UUID-2") {
		t.Errorf("log missing CUDA_VISIBLE_DEVICES output")
	}
}

func TestJobRunner_Stop(t *testing.T) {
	logDir := t.TempDir()
	job := models.Job{
		ID:      "test-job-stop",
		Command: "sleep 10",
		CWD:     logDir,
	}

	runner := NewJobRunner(job, nil, logDir)
	if err := runner.Start(); err != nil {
		t.Fatalf("failed to start: %v", err)
	}

	// Give it a moment to actually start
	time.Sleep(100 * time.Millisecond)

	pid := runner.PID()
	if pid == 0 {
		t.Errorf("expected non-zero PID")
	}

	start := time.Now()
	if err := runner.Stop(); err != nil {
		t.Errorf("stop failed: %v", err)
	}
	duration := time.Since(start)

	if duration > 1*time.Second {
		t.Errorf("stop took too long: %v", duration)
	}

	// Wait should return an error because it was signaled
	err := runner.Wait()
	if err == nil {
		t.Errorf("expected error from Wait() after Stop()")
	}
}

func TestJobRunner_CaptureOutput(t *testing.T) {
	logDir := t.TempDir()
	job := models.Job{
		ID:      "test-job-output",
		Command: "printf 'line1\nline2\n'",
		CWD:     logDir,
	}

	runner := NewJobRunner(job, nil, logDir)
	runner.Start()
	runner.Wait()

	logFile := filepath.Join(logDir, "test-job-output.log")
	f, _ := os.Open(logFile)
	defer f.Close()

	bytes, _ := io.ReadAll(f)
	if string(bytes) != "line1\nline2\n" {
		t.Errorf("expected 'line1\\nline2\\n', got %q", string(bytes))
	}
}
