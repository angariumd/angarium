package events

import (
	"encoding/json"
	"log"
	"sync"
	"time"

	"github.com/angariumd/angarium/internal/db"
	"github.com/angariumd/angarium/internal/models"
)

const (
	// Controller Events
	TypeJobSubmitted = "JOB_SUBMITTED"
	TypeJobCanceled  = "JOB_CANCELED"

	// Scheduler Events
	TypeJobAllocated   = "JOB_ALLOCATED"
	TypeLeaseExpired   = "LEASE_EXPIRED"
	TypeMaxRuntimeExcd = "MAX_RUNTIME_EXCEEDED"

	// Node/Cluster Events
	TypeNodeRegistered = "NODE_REGISTERED"
	TypeNodeRecovered  = "NODE_RECOVERED"
	TypeNodeOffline    = "NODE_OFFLINE"
	TypeJobLost        = "JOB_LOST"
)

type EventManager struct {
	db        *db.DB
	in        chan models.Event
	done      chan struct{}
	wg        sync.WaitGroup
	batchSize int
}

func New(database *db.DB) *EventManager {
	em := &EventManager{
		db:        database,
		in:        make(chan models.Event, 1000), // Buffer 1000 events
		done:      make(chan struct{}),
		batchSize: 100,
	}

	em.wg.Add(1)
	go em.loop()
	return em
}

func (em *EventManager) Close() {
	close(em.done)
	em.wg.Wait()
}

func (em *EventManager) Emit(eventType string, jobID, nodeID *string, payload any) {
	var payloadJSON *string
	if payload != nil {
		b, err := json.Marshal(payload)
		if err == nil {
			s := string(b)
			payloadJSON = &s
		}
	}

	select {
	case em.in <- models.Event{
		At:          time.Now(),
		Type:        eventType,
		JobID:       jobID,
		NodeID:      nodeID,
		PayloadJSON: payloadJSON,
	}:
	default:
		// Drop event if buffer is full to prevent blocking
		log.Printf("EventManager: Dropped event %s (buffer full)", eventType)
	}
}

func (em *EventManager) loop() {
	defer em.wg.Done()

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	var batch []models.Event

	flush := func() {
		if len(batch) == 0 {
			return
		}
		if err := em.writeBatch(batch); err != nil {
			log.Printf("EventManager: writeBatch failed: %v", err)
		}
		batch = make([]models.Event, 0, em.batchSize)
	}

	for {
		select {
		case evt := <-em.in:
			batch = append(batch, evt)
			if len(batch) >= em.batchSize {
				flush()
			}
		case <-ticker.C:
			flush()
		case <-em.done:
			flush() // Flush remaining events on exit
			return
		}
	}
}

func (em *EventManager) writeBatch(batch []models.Event) error {
	tx, err := em.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare("INSERT INTO events (at, type, job_id, node_id, payload_json) VALUES (?, ?, ?, ?, ?)")
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, e := range batch {
		_, err := stmt.Exec(e.At.UTC(), e.Type, e.JobID, e.NodeID, e.PayloadJSON)
		if err != nil {
			return err
		}
	}

	return tx.Commit()
}
