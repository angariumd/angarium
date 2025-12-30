package db

import (
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
)

type DB struct {
	conn *sql.DB
}

func Open(path string) (*DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("opening database: %w", err)
	}

	// Performance tuning
	_, err = db.Exec("PRAGMA journal_mode=WAL;")
	if err != nil {
		return nil, fmt.Errorf("setting wal mode: %w", err)
	}
	_, err = db.Exec("PRAGMA foreign_keys=ON;")
	if err != nil {
		return nil, fmt.Errorf("enabling foreign keys: %w", err)
	}

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
		addr TEXT
	);

	CREATE TABLE IF NOT EXISTS gpus (
		id TEXT PRIMARY KEY,
		node_id TEXT NOT NULL,
		idx INTEGER NOT NULL,
		uuid TEXT NOT NULL,
		name TEXT NOT NULL,
		memory_mb INTEGER NOT NULL,
		health TEXT NOT NULL DEFAULT 'OK',
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
		created_at DATETIME NOT NULL,
		queued_at DATETIME NOT NULL,
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
