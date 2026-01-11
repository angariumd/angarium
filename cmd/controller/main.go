package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/angariumd/angarium/internal/auth"
	"github.com/angariumd/angarium/internal/config"
	"github.com/angariumd/angarium/internal/controller"
	"github.com/angariumd/angarium/internal/db"
	"github.com/angariumd/angarium/internal/scheduler"
)

func main() {
	configPath := flag.String("config", "config/controller.yaml", "path to controller config")
	flag.Parse()

	cfg, err := config.LoadControllerConfig(*configPath)
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}

	database, err := db.Open(cfg.DBPath)
	if err != nil {
		log.Fatalf("failed to open db: %v", err)
	}
	defer database.Close()

	if err := database.Init(); err != nil {
		log.Fatalf("failed to init db: %v", err)
	}

	// Seed users from config
	for _, u := range cfg.Users {
		_, err := database.Exec("INSERT OR REPLACE INTO users (id, name, token_hash) VALUES (?, ?, ?)", u.ID, u.Name, u.Token)
		if err != nil {
			log.Fatalf("failed to seed user %s: %v", u.Name, err)
		}
	}

	authenticator := auth.NewAuthenticator(database)
	server := controller.NewServer(database, authenticator, cfg.SharedToken)

	// Start scheduler
	sched := scheduler.New(database, cfg.SharedToken)
	go sched.Run(context.Background(), 2*time.Second)

	// Start stale node detector
	server.StartStaleNodeDetector(10 * time.Second)

	// Start reconciliation loop
	server.StartReconciliationLoop(30 * time.Second)

	fmt.Printf("Angarium Controller listening on %s\n", cfg.Addr)
	log.Fatal(http.ListenAndServe(cfg.Addr, server.Routes()))
}
