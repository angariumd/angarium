package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/angariumd/angarium/internal/auth"
	"github.com/angariumd/angarium/internal/config"
	"github.com/angariumd/angarium/internal/controller"
	"github.com/angariumd/angarium/internal/db"
	"github.com/angariumd/angarium/internal/events"
	"github.com/angariumd/angarium/internal/scheduler"
)

func main() {
	configPath := flag.String("config", "config/controller.yaml", "path to controller config")
	flag.Parse()

	cfg, err := config.LoadControllerConfig(*configPath)
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}

	http.DefaultTransport.(*http.Transport).TLSClientConfig = &tls.Config{InsecureSkipVerify: true}

	database, err := db.Open(cfg.DBPath)
	if err != nil {
		log.Fatalf("failed to open db: %v", err)
	}
	defer database.Close()

	if err := database.Init(); err != nil {
		log.Fatalf("failed to init db: %v", err)
	}

	for _, u := range cfg.Users {
		_, err := database.Exec("INSERT OR REPLACE INTO users (id, name, token_hash) VALUES (?, ?, ?)", u.ID, u.Name, u.Token)
		if err != nil {
			log.Fatalf("failed to seed user %s: %v", u.Name, err)
		}
	}

	authenticator := auth.NewAuthenticator(database)
	eventMgr := events.New(database)
	defer eventMgr.Close()

	server := controller.NewServer(database, authenticator, eventMgr, cfg.SharedToken)

	sched := scheduler.New(database, eventMgr, cfg.SharedToken)
	go sched.Run(context.Background(), 2*time.Second)

	server.StartStaleNodeDetector(10 * time.Second)

	server.StartReconciliationLoop(30 * time.Second)

	fmt.Printf("Angarium Controller listening on %s (TLS: %v)\n", cfg.Addr, cfg.CertPath != "")
	if cfg.CertPath != "" && cfg.KeyPath != "" {
		log.Fatal(http.ListenAndServeTLS(cfg.Addr, cfg.CertPath, cfg.KeyPath, server.Routes()))
	} else {
		log.Fatal(http.ListenAndServe(cfg.Addr, server.Routes()))
	}
}
