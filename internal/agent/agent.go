package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/angariumd/angarium/internal/models"
)

type Agent struct {
	nodeID        string
	controllerURL string
	gpuProvider   GPUProvider
	version       string
	addr          string
}

func NewAgent(nodeID, controllerURL string, gpuProvider GPUProvider, version, addr string) *Agent {
	return &Agent{
		nodeID:        nodeID,
		controllerURL: controllerURL,
		gpuProvider:   gpuProvider,
		version:       version,
		addr:          addr,
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
