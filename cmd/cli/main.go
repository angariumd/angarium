package main

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/angariumd/angarium/internal/config"
	"github.com/angariumd/angarium/internal/models"
	"github.com/spf13/cobra"
)

var (
	controllerURL string
	token         string
)

func main() {
	// Load config from file
	cfg, _ := config.LoadCLIConfig()

	controllerURL = os.Getenv("ANGARIUM_CONTROLLER")
	if controllerURL == "" {
		controllerURL = cfg.ControllerURL
	}
	if controllerURL == "" {
		controllerURL = "http://localhost:8080"
	}

	token = os.Getenv("ANGARIUM_TOKEN")
	if token == "" {
		token = cfg.Token
	}
	if token == "" {
		token = "sam-secret-token" // Default developer token
	}

	rootCmd := &cobra.Command{Use: "angarium"}

	psCmd := &cobra.Command{
		Use:   "ps",
		Short: "List running jobs",
		RunE:  listRunningJobs,
	}

	var follow bool
	logsCmd := &cobra.Command{
		Use:   "logs <job_id>",
		Short: "Print the logs for a job",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return showLogs(args[0], follow)
		},
	}
	logsCmd.Flags().BoolVarP(&follow, "follow", "f", false, "Specify if the logs should be streamed")

	cancelCmd := &cobra.Command{
		Use:   "cancel <job_id>",
		Short: "Cancel a job",
		Args:  cobra.ExactArgs(1),
		RunE:  cancelJob,
	}

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

	loginCmd := &cobra.Command{
		Use:   "login <token>",
		Short: "Login with an API token",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, _ := config.LoadCLIConfig()
			cfg.Token = args[0]
			cfg.ControllerURL = controllerURL
			if err := config.SaveCLIConfig(cfg); err != nil {
				return err
			}
			fmt.Printf("Logged in to %s\n", controllerURL)
			return nil
		},
	}

	whoamiCmd := &cobra.Command{
		Use:   "whoami",
		Short: "Show current user and controller",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Printf("Controller: %s\n", controllerURL)
			fmt.Printf("Token:      %s... (prefix)\n", token[:min(len(token), 8)])
			return nil
		},
	}

	rootCmd.AddCommand(nodesCmd, queueCmd, submitCmd, statusCmd, inspectCmd, psCmd, logsCmd, cancelCmd, loginCmd, whoamiCmd)

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

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NODE ID\tSTATUS\tADDR\tGPUS (FREE/TOTAL)")
	for _, n := range nodes {
		fmt.Fprintf(w, "%s\t%s\t%s\t%d/%d\n", n.ID, n.Status, n.Addr, n.GPUFree, n.GPUCount)
	}
	return w.Flush()
}

func resolveJobID(prefix string) (string, error) {
	resp, err := request("GET", "/v1/jobs", nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var jobs []models.Job
	if err := json.NewDecoder(resp.Body).Decode(&jobs); err != nil {
		return "", err
	}

	for _, j := range jobs {
		if strings.HasPrefix(j.ID, prefix) {
			return j.ID, nil
		}
	}
	return "", fmt.Errorf("job not found: %s", prefix)
}

func inspectJob(cmd *cobra.Command, args []string) error {
	fullID, err := resolveJobID(args[0])
	if err != nil {
		return err
	}

	resp, err := request("GET", "/v1/jobs", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	var jobs []models.Job
	if err := json.NewDecoder(resp.Body).Decode(&jobs); err != nil {
		return err
	}

	for _, target := range jobs {
		if target.ID == fullID {
			fmt.Printf("Job ID:     %s\n", target.ID)
			fmt.Printf("Owner:      %s\n", target.OwnerID)
			fmt.Printf("State:      %s\n", target.State)
			fmt.Printf("GPU Count:  %d\n", target.GPUCount)
			fmt.Printf("Command:    %s\n", target.Command)
			fmt.Printf("CWD:        %s\n", target.CWD)
			fmt.Printf("Created:    %s\n", target.CreatedAt.Format(time.RFC3339))
			if target.StartedAt != nil {
				fmt.Printf("Started:    %s\n", target.StartedAt.Format(time.RFC3339))
			}
			if target.FinishedAt != nil {
				fmt.Printf("Finished:   %s\n", target.FinishedAt.Format(time.RFC3339))
			}
			if target.ExitCode != nil {
				fmt.Printf("Exit Code:  %d\n", *target.ExitCode)
			}
			if target.Reason != nil {
				fmt.Printf("Reason:     %s\n", *target.Reason)
			}
			return nil
		}
	}
	return fmt.Errorf("job not found: %s", fullID)
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

func listRunningJobs(cmd *cobra.Command, args []string) error {
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
	fmt.Fprintln(w, "ID\tSTATE\tOWNER\tGPUS\tCOMMAND")
	for _, j := range jobs {
		if j.State == models.JobStateRunning || j.State == models.JobStateStarting {
			fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%s\n", j.ID[:8], j.State, j.OwnerID, j.GPUCount, j.Command)
		}
	}
	return w.Flush()
}

func showLogs(jobID string, follow bool) error {
	fullID, err := resolveJobID(jobID)
	if err != nil {
		return err
	}

	path := fmt.Sprintf("/v1/jobs/%s/logs", fullID)
	if follow {
		path += "?follow=true"
	}

	resp, err := request("GET", path, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("logs failed (%d): %s", resp.StatusCode, string(b))
	}

	_, err = io.Copy(os.Stdout, resp.Body)
	return err
}

func cancelJob(cmd *cobra.Command, args []string) error {
	fullID, err := resolveJobID(args[0])
	if err != nil {
		return err
	}

	resp, err := request("POST", fmt.Sprintf("/v1/jobs/%s/cancel", fullID), nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("cancel failed (%d): %s", resp.StatusCode, string(b))
	}

	fmt.Printf("Job %s cancel request sent.\n", fullID)
	return nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func request(method, path string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequest(method, controllerURL+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("unauthorized: check your ANGARIUM_TOKEN")
	}

	return resp, nil
}
