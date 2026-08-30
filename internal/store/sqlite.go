package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"beaconsubs/internal/jobs"
	_ "modernc.org/sqlite"
)

type SQLite struct {
	db *sql.DB
}

func Open(path string) (*SQLite, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, fmt.Errorf("create database directory: %w", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open SQLite: %w", err)
	}
	db.SetMaxOpenConns(1)
	store := &SQLite{db: db}
	if err := store.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *SQLite) migrate() error {
	statements := []string{
		`PRAGMA journal_mode=WAL`,
		`PRAGMA foreign_keys=ON`,
		`PRAGMA busy_timeout=5000`,
		`CREATE TABLE IF NOT EXISTS jobs (
			id TEXT PRIMARY KEY,
			status TEXT NOT NULL,
			external_id TEXT NOT NULL DEFAULT '',
			payload BLOB NOT NULL,
			callback_url TEXT NOT NULL DEFAULT '',
			overwrite_existing INTEGER NOT NULL DEFAULT 0,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS jobs_created_at_idx ON jobs(created_at DESC)`,
		`CREATE INDEX IF NOT EXISTS jobs_external_id_idx ON jobs(external_id)`,
	}
	for _, statement := range statements {
		if _, err := s.db.Exec(statement); err != nil {
			return fmt.Errorf("initialize SQLite: %w", err)
		}
	}
	return nil
}

func (s *SQLite) Save(job *jobs.Job) error {
	payload, err := json.Marshal(job)
	if err != nil {
		return fmt.Errorf("encode job %s: %w", job.ID, err)
	}
	_, err = s.db.ExecContext(context.Background(), `
		INSERT INTO jobs (id,status,external_id,payload,callback_url,overwrite_existing,created_at,updated_at)
		VALUES (?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET
			status=excluded.status,
			external_id=excluded.external_id,
			payload=excluded.payload,
			callback_url=excluded.callback_url,
			overwrite_existing=excluded.overwrite_existing,
			updated_at=excluded.updated_at`,
		job.ID, job.Status, job.ExternalID, payload, job.CallbackURL, job.Overwrite,
		job.CreatedAt.Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("save job %s: %w", job.ID, err)
	}
	return nil
}

func (s *SQLite) Load() ([]*jobs.Job, error) {
	rows, err := s.db.Query(`SELECT payload,callback_url,overwrite_existing FROM jobs ORDER BY created_at DESC LIMIT 1000`)
	if err != nil {
		return nil, fmt.Errorf("load jobs: %w", err)
	}
	defer rows.Close()
	var result []*jobs.Job
	for rows.Next() {
		var payload []byte
		var callbackURL string
		var overwrite bool
		if err := rows.Scan(&payload, &callbackURL, &overwrite); err != nil {
			return nil, err
		}
		var job jobs.Job
		if err := json.Unmarshal(payload, &job); err != nil {
			return nil, fmt.Errorf("decode stored job: %w", err)
		}
		job.CallbackURL = callbackURL
		job.Overwrite = overwrite
		result = append(result, &job)
	}
	return result, rows.Err()
}

func (s *SQLite) Close() error { return s.db.Close() }
