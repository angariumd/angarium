package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/angariumd/angarium/internal/models"
	"github.com/spf13/cobra"
)

var (
	controllerURL string
	token         string
)

func main() {
	controllerURL = os.Getenv("GPU_CONTROLLER")
	if controllerURL == "" {
		controllerURL = "http://localhost:8080"
	}
	token = os.Getenv("GPU_TOKEN")
	if token == "" {
		token = "sam-secret-token" // Default for MVP seed
	}

	rootCmd := &cobra.Command{Use: "cli"}

	nodesCmd := &cobra.Command{
		Use:   "nodes",
		Short: "List cluster nodes",
		RunE:  listNodes,
	}

	queueCmd := &cobra.Command{
		Use:   "queue",
		Short: "List job queue",
		RunE:  listQueue,
	}

	var gpuCount int
	var cwd string
	submitCmd := &cobra.Command{
		Use:   "submit [command...]",
		Short: "Submit a new job",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return submitJob(gpuCount, cwd, strings.Join(args, " "))
		},
	}
	submitCmd.Flags().IntVar(&gpuCount, "gpus", 1, "Number of GPUs requested")
	submitCmd.Flags().StringVar(&cwd, "cwd", "", "Current working directory (mandatory)")
	submitCmd.MarkFlagRequired("cwd")

	statusCmd := &cobra.Command{
		Use:   "status",
		Short: "Show cluster status (GPUs capacity and usage)",
		RunE:  showStatus,
	}

	inspectCmd := &cobra.Command{
		Use:   "inspect <job_id>",
		Short: "Show detailed job information",
		Args:  cobra.ExactArgs(1),
		RunE:  inspectJob,
	}

	rootCmd.AddCommand(nodesCmd, queueCmd, submitCmd, statusCmd, inspectCmd)

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func listNodes(cmd *cobra.Command, args []string) error {
	resp, err := request("GET", "/v1/nodes", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	var nodes []models.Node
	if err := json.NewDecoder(resp.Body).Decode(&nodes); err != nil {
		return err
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tSTATUS\tLAST HEARTBEAT\tADDR")
	for _, n := range nodes {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", n.ID, n.Status, n.LastHeartbeatAt.Format("15:04:05"), n.Addr)
	}
	return w.Flush()
}

func listQueue(cmd *cobra.Command, args []string) error {
	resp, err := request("GET", "/v1/jobs", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	var jobs []models.Job
	if err := json.NewDecoder(resp.Body).Decode(&jobs); err != nil {
		return err
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tSTATE\tOWNER\tGPUS\tCOMMAND\tREASON")
	for _, j := range jobs {
		reason := "-"
		if j.Reason != nil {
			reason = *j.Reason
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%s\t%s\n", j.ID[:8], j.State, j.OwnerID, j.GPUCount, j.Command, reason)
	}
	return w.Flush()
}

func showStatus(cmd *cobra.Command, args []string) error {
	resp, err := request("GET", "/v1/nodes", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	var nodes []models.Node
	if err := json.NewDecoder(resp.Body).Decode(&nodes); err != nil {
		return err
	}

	// The nodes endpoint currently only returns node info.
	// We need another one or update handleNodeList to include GPUs.

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NODE ID\tSTATUS\tADDR\tGPUS (FREE/TOTAL)")
	for _, n := range nodes {
		fmt.Fprintf(w, "%s\t%s\t%s\t%d/%d\n", n.ID, n.Status, n.Addr, n.GPUFree, n.GPUCount)
	}
	return w.Flush()
}

func inspectJob(cmd *cobra.Command, args []string) error {
	jobID := args[0]
	resp, err := request("GET", "/v1/jobs?id="+jobID, nil) // Simple query for now
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	var jobs []models.Job
	if err := json.NewDecoder(resp.Body).Decode(&jobs); err != nil {
		return err
	}

	var target *models.Job
	for i := range jobs {
		if strings.HasPrefix(jobs[i].ID, jobID) {
			target = &jobs[i]
			break
		}
	}

	if target == nil {
		return fmt.Errorf("job not found: %s", jobID)
	}

	fmt.Printf("Job ID:     %s\n", target.ID)
	fmt.Printf("Owner:      %s\n", target.OwnerID)
	fmt.Printf("State:      %s\n", target.State)
	fmt.Printf("GPU Count:  %d\n", target.GPUCount)
	fmt.Printf("Command:    %s\n", target.Command)
	fmt.Printf("CWD:        %s\n", target.CWD)
	fmt.Printf("Created:    %s\n", target.CreatedAt.Format(time.RFC3339))
	if target.Reason != nil {
		fmt.Printf("Reason:     %s\n", *target.Reason)
	}
	return nil
}

func submitJob(gpuCount int, cwd, command string) error {
	reqBody := map[string]any{
		"gpu_count": gpuCount,
		"cwd":       cwd,
		"command":   command,
	}
	body, _ := json.Marshal(reqBody)

	resp, err := request("POST", "/v1/jobs", bytes.NewBuffer(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("submission failed (%d): %s", resp.StatusCode, string(b))
	}

	var res map[string]string
	json.NewDecoder(resp.Body).Decode(&res)
	fmt.Printf("Job submitted! ID: %s\n", res["id"])
	return nil
}

func request(method, path string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequest(method, controllerURL+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("unauthorized: check your GPU_TOKEN")
	}

	return resp, nil
}
