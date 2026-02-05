package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

var (
	controllerURL string
	token         string
	concurrency   int
	jobsPerWorker int
)

func init() {
	flag.StringVar(&controllerURL, "url", "http://localhost:8090", "Controller URL")
	flag.StringVar(&token, "token", "sam-secret-token", "Auth token")
	flag.IntVar(&concurrency, "c", 10, "Number of concurrent workers")
	flag.IntVar(&jobsPerWorker, "n", 10, "Jobs per worker")
}

type JobRequest struct {
	Command  string `json:"command"`
	GPUCount int    `json:"gpu_count"`
	CWD      string `json:"cwd"`
}

type JobResponse struct {
	ID string `json:"id"`
}

// Stats
var (
	successCount int64
	failCount    int64
	totalLatency int64 // microseconds
)

func main() {
	flag.Parse()

	totalJobs := concurrency * jobsPerWorker
	fmt.Printf("Starting load test: %d workers, %d jobs each (%d total)\n", concurrency, jobsPerWorker, totalJobs)
	fmt.Printf("Target: %s\n", controllerURL)

	start := time.Now()
	var wg sync.WaitGroup

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			worker(id)
		}(i)
	}

	wg.Wait()
	duration := time.Since(start)

	tps := float64(totalJobs) / duration.Seconds()
	avgLatency := time.Duration(atomic.LoadInt64(&totalLatency) / int64(totalJobs))

	fmt.Printf("\n--- Results (%d total) ---\n", totalJobs)
	fmt.Printf("Duration: %v (%.2f ops/sec)\n", duration, tps)
	fmt.Printf("Latency:  avg=%v\n", avgLatency)
	fmt.Printf("Status:   %d ok, %d failed\n", atomic.LoadInt64(&successCount), atomic.LoadInt64(&failCount))
}

func worker(id int) {
	client := &http.Client{Timeout: 5 * time.Second}

	for j := 0; j < jobsPerWorker; j++ {
		reqBody := JobRequest{
			Command:  "echo 'loadtest'",
			GPUCount: 1,
			CWD:      "/tmp",
		}
		jsonData, _ := json.Marshal(reqBody)

		reqStart := time.Now()
		req, _ := http.NewRequest("POST", fmt.Sprintf("%s/v1/jobs", controllerURL), bytes.NewBuffer(jsonData))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")

		resp, err := client.Do(req)
		latency := time.Since(reqStart)
		atomic.AddInt64(&totalLatency, int64(latency))

		if err != nil || resp.StatusCode != http.StatusOK {
			atomic.AddInt64(&failCount, 1)
			if err != nil {
				// reduce spam
				if j%10 == 0 {
					fmt.Printf("[W%d] Error: %v\n", id, err)
				}
			} else {
				fmt.Printf("[W%d] Status: %d\n", id, resp.StatusCode)
				resp.Body.Close()
			}
			continue
		}

		// consume body to reuse keep-alive
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()

		atomic.AddInt64(&successCount, 1)
	}
}

// TODO(sam): add support for multiple GPU counts in the mix-in
