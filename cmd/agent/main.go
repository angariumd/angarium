package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/angariumd/angarium/internal/agent"
	"github.com/angariumd/angarium/internal/config"
)

func main() {
	configPath := flag.String("config", "config/agent.yaml", "path to agent config")
	flag.Parse()

	cfg, err := config.LoadAgentConfig(*configPath)
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}

	// Use fake provider for MVP / development
	nodeID, _ := os.Hostname()
	if cfg.NodeID != "" {
		nodeID = cfg.NodeID
	}

	gpuProvider := agent.NewFakeGPUProvider(nodeID)
	a := agent.NewAgent(nodeID, cfg.ControllerURL, gpuProvider, "v0.1.0", "http://localhost:8081")

	fmt.Printf("Angarium Agent starting (Node ID: %s)...\n", nodeID)
	a.StartHeartbeat(5 * time.Second)

	// Keep agent running
	select {}
}
