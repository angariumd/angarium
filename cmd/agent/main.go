package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
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

	// Initialize GPU inventory and agent
	nodeID, _ := os.Hostname()
	if cfg.NodeID != "" {
		nodeID = cfg.NodeID
	}

	gpuProvider := agent.NewFakeGPUProvider(nodeID)
	a := agent.NewAgent(nodeID, cfg.ControllerURL, gpuProvider, "v0.1.0", cfg.Addr, cfg.SharedToken)

	fmt.Printf("Angarium Agent starting (Node ID: %s, Addr: %s)...\n", nodeID, cfg.Addr)
	a.StartHeartbeat(5 * time.Second)

	// Initialize API server on designated port
	port := "8081"

	// Just use 8081 for now
	go func() {
		log.Printf("Agent API listening on :%s", port)
		if err := http.ListenAndServe(":"+port, a.Routes()); err != nil {
			log.Fatalf("agent server failed: %v", err)
		}
	}()

	// Keep agent running
	select {}
}
