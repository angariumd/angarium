package main

import (
	"crypto/tls"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/angariumd/angarium/internal/agent"
	"github.com/angariumd/angarium/internal/config"
)

func main() {
	configPath := flag.String("config", "config/agent.yaml", "path to agent config")
	mockFlag := flag.Bool("mock", false, "force mock GPU provider (for development only)")
	flag.Parse()

	cfg, err := config.LoadAgentConfig(*configPath)
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}

	// Accept self-signed certs for internal communication
	http.DefaultTransport.(*http.Transport).TLSClientConfig = &tls.Config{InsecureSkipVerify: true}

	nodeID, _ := os.Hostname()
	if cfg.NodeID != "" {
		nodeID = cfg.NodeID
	}

	var gpuProvider agent.GPUProvider
	if *mockFlag {
		log.Println("WARNING: Running in MOCK mode with simulated GPUs. DO NOT USE IN PRODUCTION.")
		gpuProvider = agent.NewMockGPUProvider(nodeID)
	} else {
		// Strict production path
		if _, err := exec.LookPath("nvidia-smi"); err != nil {
			log.Fatal("FATAL: nvidia-smi not found. Compute nodes require NVIDIA drivers. Use --mock for testing.")
		}

		// Verify GPU communication
		if err := exec.Command("nvidia-smi", "-L").Run(); err != nil {
			log.Fatal("FATAL: nvidia-smi found but failed to communicate with GPUs. Check drivers.")
		}

		gpuProvider = &agent.NvidiaGPUProvider{}
		log.Printf("Successfully initialized real NvidiaGPUProvider")
	}

	a := agent.NewAgent(nodeID, cfg.ControllerURL, gpuProvider, "v0.1.0", cfg.Addr, cfg.SharedToken)

	fmt.Printf("Angarium Agent starting (Node ID: %s, Addr: %s)...\n", nodeID, cfg.Addr)
	a.StartHeartbeat(5 * time.Second)

	go func() {
		listenAddr := cfg.Addr
		if strings.HasPrefix(listenAddr, "http://") {
			listenAddr = strings.TrimPrefix(listenAddr, "http://")
		} else if strings.HasPrefix(listenAddr, "https://") {
			listenAddr = strings.TrimPrefix(listenAddr, "https://")
		}

		log.Printf("Agent API listening on %s (TLS: %v)", listenAddr, cfg.CertPath != "")
		var err error
		if cfg.CertPath != "" && cfg.KeyPath != "" {
			err = http.ListenAndServeTLS(listenAddr, cfg.CertPath, cfg.KeyPath, a.Routes())
		} else {
			err = http.ListenAndServe(listenAddr, a.Routes())
		}
		if err != nil {
			log.Fatalf("agent server failed: %v", err)
		}
	}()

	// Keep agent running
	select {}
}
