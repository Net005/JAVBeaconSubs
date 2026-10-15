package store

import (
	"encoding/json"
	"fmt"
	"javbeaconsubs/internal/jobs"
	"time"
)

func (s *SQLite) migrateActivity() error {
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS job_activity (
		id TEXT PRIMARY KEY REFERENCES jobs(id) ON DELETE CASCADE,
		status TEXT NOT NULL,
		summary_payload BLOB NOT NULL,
		search_text TEXT NOT NULL,
		created_at TEXT NOT NULL
	)`); err != nil {
		return err
	}
	rows, err := s.db.Query(`PRAGMA table_info(jobs)`)
	if err != nil {
		return err
	}
	columns := map[string]bool{}
	for rows.Next() {
		var cid, notnull, pk int
		var name, kind string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &kind, &notnull, &defaultValue, &pk); err != nil {
			rows.Close()
			return err
		}
		columns[name] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	// Reuse records from the first activity schema, if present, without
	// rewriting or dropping any existing job columns.
	if columns["summary_payload"] && columns["search_text"] {
		if _, err := s.db.Exec(`INSERT INTO job_activity(id,status,summary_payload,search_text,created_at)
			SELECT id,status,summary_payload,search_text,created_at FROM jobs
			WHERE summary_payload IS NOT NULL AND NOT EXISTS(SELECT 1 FROM job_activity WHERE job_activity.id=jobs.id)`); err != nil {
			return err
		}
	}
	// Backfill once, in bounded batches, without changing durable job state.
	for {
		rows, err := s.db.Query(`SELECT payload FROM jobs WHERE NOT EXISTS(SELECT 1 FROM job_activity WHERE job_activity.id=jobs.id AND json_extract(summary_payload, '$.skipped_count') IS NOT NULL AND json_type(summary_payload, '$.recent_file_results') IS NOT NULL) LIMIT 100`)
		if err != nil {
			return err
		}
		var batch []*jobs.Job
		for rows.Next() {
			var payload []byte
			var job jobs.Job
			if err := rows.Scan(&payload); err != nil {
				rows.Close()
				return err
			}
			if err := json.Unmarshal(payload, &job); err != nil {
				rows.Close()
				return fmt.Errorf("migrate activity record: %w", err)
			}
			batch = append(batch, &job)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if len(batch) == 0 {
			break
		}
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		for _, job := range batch {
			payload, err := json.Marshal(jobs.Summary(job))
			if err == nil {
				_, err = tx.Exec(`INSERT INTO job_activity(id,status,summary_payload,search_text,created_at) VALUES(?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET summary_payload=excluded.summary_payload`, job.ID, job.Status, payload, jobs.SearchText(job), job.CreatedAt.Format(time.RFC3339Nano))
			}
			if err != nil {
				tx.Rollback()
				return err
			}
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	for _, query := range []string{
		`CREATE INDEX IF NOT EXISTS activity_status_idx ON job_activity(status)`,
		`CREATE INDEX IF NOT EXISTS activity_order_idx ON job_activity(CASE status WHEN 'running' THEN 0 WHEN 'queued' THEN 1 ELSE 2 END ASC, CASE WHEN status = 'queued' THEN created_at END ASC, created_at DESC)`,
		`CREATE INDEX IF NOT EXISTS activity_active_order_idx ON job_activity(CASE status WHEN 'running' THEN 0 WHEN 'queued' THEN 1 ELSE 2 END ASC, CASE WHEN status = 'queued' THEN created_at END ASC, created_at DESC) WHERE status IN ('queued','running')`,
		`CREATE INDEX IF NOT EXISTS activity_completed_order_idx ON job_activity(created_at DESC) WHERE status IN ('complete','skipped')`,
		`CREATE INDEX IF NOT EXISTS activity_failed_order_idx ON job_activity(created_at DESC) WHERE status IN ('failed','cancelled')`,
	} {
		if _, err := s.db.Exec(query); err != nil {
			return err
		}
	}
	return nil
}
