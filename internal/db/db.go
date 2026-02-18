package db

import (
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

type DB struct {
	conn *sql.DB
}

func Open(path string) (*DB, error) {
	// _txlock=immediate avoids deadlocks during concurrent writes
	dsn := fmt.Sprintf("%s?_txlock=immediate&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("opening database: %w", err)
	}

	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(25)
	db.SetConnMaxLifetime(5 * time.Minute)

	return &DB{conn: db}, nil
}

func (d *DB) Close() error {
	return d.conn.Close()
}

func (d *DB) Init() error {
	schema := `
	CREATE TABLE IF NOT EXISTS nodes (
		id TEXT PRIMARY KEY,
		status TEXT NOT NULL,
		last_heartbeat_at DATETIME NOT NULL,
		agent_version TEXT NOT NULL,
		addr TEXT,
		memory_total_mb INTEGER NOT NULL DEFAULT 0,
		memory_used_mb INTEGER NOT NULL DEFAULT 0
	);

	CREATE TABLE IF NOT EXISTS gpus (
		id TEXT PRIMARY KEY,
		node_id TEXT NOT NULL,
		idx INTEGER NOT NULL,
		uuid TEXT NOT NULL,
		name TEXT NOT NULL,
		memory_mb INTEGER NOT NULL,
		health TEXT NOT NULL DEFAULT 'OK',
		utilization INTEGER NOT NULL DEFAULT 0,
		memory_used_mb INTEGER NOT NULL DEFAULT 0,
		last_seen_at DATETIME NOT NULL,
		FOREIGN KEY (node_id) REFERENCES nodes(id) ON DELETE CASCADE
	);

	CREATE TABLE IF NOT EXISTS users (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		token_hash TEXT NOT NULL
	);

	CREATE TABLE IF NOT EXISTS jobs (
		id TEXT PRIMARY KEY,
		owner_id TEXT NOT NULL,
		state TEXT NOT NULL,
		priority INTEGER NOT NULL DEFAULT 0,
		gpu_count INTEGER NOT NULL,
		command TEXT NOT NULL,
		cwd TEXT NOT NULL,
		env_json TEXT NOT NULL,
		retry_count INTEGER NOT NULL DEFAULT 0,
		max_runtime_minutes INTEGER NOT NULL DEFAULT 0,
		created_at DATETIME NOT NULL,
		queued_at DATETIME NOT NULL,
		started_at DATETIME,
		finished_at DATETIME,
		exit_code INTEGER,
		reason TEXT,
		FOREIGN KEY (owner_id) REFERENCES users(id)
	);

	CREATE TABLE IF NOT EXISTS events (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		at DATETIME NOT NULL,
		type TEXT NOT NULL,
		job_id TEXT,
		node_id TEXT,
		payload_json TEXT
	);

	CREATE INDEX IF NOT EXISTS idx_events_job_id ON events(job_id);
	CREATE INDEX IF NOT EXISTS idx_events_node_id ON events(node_id);
	CREATE INDEX IF NOT EXISTS idx_events_at ON events(at);

	CREATE TABLE IF NOT EXISTS allocations (
		id TEXT PRIMARY KEY,
		job_id TEXT NOT NULL,
		node_id TEXT NOT NULL,
		status TEXT NOT NULL,
		created_at DATETIME NOT NULL,
		released_at DATETIME,
		FOREIGN KEY (job_id) REFERENCES jobs(id),
		FOREIGN KEY (node_id) REFERENCES nodes(id)
	);

	CREATE TABLE IF NOT EXISTS gpu_leases (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		gpu_id TEXT NOT NULL,
		allocation_id TEXT NOT NULL,
		leased_at DATETIME NOT NULL,
		expires_at DATETIME NOT NULL,
		FOREIGN KEY (gpu_id) REFERENCES gpus(id),
		FOREIGN KEY (allocation_id) REFERENCES allocations(id) ON DELETE CASCADE
	);
	`

	_, err := d.conn.Exec(schema)
	if err != nil {
		return fmt.Errorf("initializing schema: %w", err)
	}

	return nil
}

func (d *DB) Exec(query string, args ...any) (sql.Result, error) {
	return d.conn.Exec(query, args...)
}

func (d *DB) QueryRow(query string, args ...any) *sql.Row {
	return d.conn.QueryRow(query, args...)
}

func (d *DB) Query(query string, args ...any) (*sql.Rows, error) {
	return d.conn.Query(query, args...)
}

func (d *DB) Begin() (*sql.Tx, error) {
	return d.conn.Begin()
}
