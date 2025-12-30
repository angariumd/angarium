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

	rootCmd.AddCommand(nodesCmd, queueCmd, submitCmd)

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
	fmt.Fprintln(w, "ID\tSTATE\tOWNER\tGPUS\tCOMMAND")
	for _, j := range jobs {
		fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%s\n", j.ID[:8], j.State, j.OwnerID, j.GPUCount, j.Command)
	}
	return w.Flush()
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
