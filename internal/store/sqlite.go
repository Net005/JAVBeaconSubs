package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"javbeaconsubs/internal/auth"
	"javbeaconsubs/internal/config"
	"javbeaconsubs/internal/jobs"
	profilecatalog "javbeaconsubs/internal/profile"
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
		`CREATE TABLE IF NOT EXISTS settings (
			key TEXT PRIMARY KEY,
			value BLOB NOT NULL,
			updated_at TEXT NOT NULL
		)`,
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

func (s *SQLite) ListPage(page, pageSize int, filter string) ([]*jobs.Job, int, error) {
	if page < 1 {
		page = 1
	}
	if pageSize != 25 && pageSize != 50 && pageSize != 100 {
		pageSize = 50
	}
	where, args := "", []any{}
	if strings.TrimSpace(filter) != "" {
		where = ` WHERE lower(replace(id || ' ' || external_id || ' ' || coalesce(json_extract(payload,'$.current_file'),'') || ' ' || coalesce(json_extract(payload,'$.current_path'),'') || ' ' || coalesce((SELECT group_concat(value,' ') FROM json_each(payload,'$.files')),''),'\','/')) LIKE ? ESCAPE '\'`
		args = append(args, wildcardLike(filter))
	}
	var total int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM jobs`+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count jobs: %w", err)
	}
	queryArgs := append(append([]any{}, args...), pageSize, (page-1)*pageSize)
	rows, err := s.db.Query(`SELECT payload,callback_url,overwrite_existing FROM jobs`+where+` ORDER BY created_at DESC LIMIT ? OFFSET ?`, queryArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("list jobs: %w", err)
	}
	defer rows.Close()
	result, err := decodeJobs(rows)
	return result, total, err
}

func (s *SQLite) GetJob(id string) (*jobs.Job, bool, error) {
	var payload []byte
	var callbackURL string
	var overwrite bool
	err := s.db.QueryRow(`SELECT payload,callback_url,overwrite_existing FROM jobs WHERE id = ?`, id).Scan(&payload, &callbackURL, &overwrite)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var job jobs.Job
	if err := json.Unmarshal(payload, &job); err != nil {
		return nil, false, fmt.Errorf("decode stored job: %w", err)
	}
	job.CallbackURL, job.Overwrite = callbackURL, overwrite
	return &job, true, nil
}

func wildcardLike(value string) string {
	value = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(value), "\\", "/"))
	var output strings.Builder
	if !strings.Contains(value, "*") {
		output.WriteByte('%')
	}
	for _, r := range value {
		switch r {
		case '*':
			output.WriteByte('%')
		case '%', '_', '\\':
			output.WriteByte('\\')
			output.WriteRune(r)
		default:
			output.WriteRune(r)
		}
	}
	if !strings.Contains(value, "*") {
		output.WriteByte('%')
	}
	return output.String()
}

type rowScanner interface {
	Next() bool
	Scan(...any) error
	Err() error
}

func decodeJobs(rows rowScanner) ([]*jobs.Job, error) {
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
		job.CallbackURL, job.Overwrite = callbackURL, overwrite
		result = append(result, &job)
	}
	return result, rows.Err()
}

func (s *SQLite) Close() error { return s.db.Close() }

func (s *SQLite) LoadTranslation() (config.TranslationConfig, bool, error) {
	var payload []byte
	err := s.db.QueryRow(`SELECT value FROM settings WHERE key = 'translation'`).Scan(&payload)
	if err == sql.ErrNoRows {
		return config.TranslationConfig{}, false, nil
	}
	if err != nil {
		return config.TranslationConfig{}, false, fmt.Errorf("load translation settings: %w", err)
	}
	var value config.TranslationConfig
	if err := json.Unmarshal(payload, &value); err != nil {
		return value, false, fmt.Errorf("decode translation settings: %w", err)
	}
	return value, true, nil
}

func (s *SQLite) SaveTranslation(value config.TranslationConfig) error {
	return s.saveSetting("translation", value)
}

func (s *SQLite) LoadPostProcessing() (config.PostProcessingConfig, bool, error) {
	var value config.PostProcessingConfig
	ok, err := s.loadSetting("post_processing", &value)
	return value, ok, err
}

func (s *SQLite) SavePostProcessing(value config.PostProcessingConfig) error {
	return s.saveSetting("post_processing", value)
}

func (s *SQLite) LoadProfiles() (config.ProfilesConfig, bool, error) {
	var value config.ProfilesConfig
	ok, err := s.loadSetting("profiles", &value)
	return value, ok, err
}

func (s *SQLite) SaveProfiles(value config.ProfilesConfig) error {
	return s.saveSetting("profiles", value)
}

func (s *SQLite) LoadJAVBeacon() (config.JAVBeaconConfig, bool, error) {
	var value config.JAVBeaconConfig
	ok, err := s.loadSetting("javbeacon", &value)
	return value, ok, err
}

func (s *SQLite) SaveJAVBeacon(value config.JAVBeaconConfig) error {
	return s.saveSetting("javbeacon", value)
}

func (s *SQLite) LoadRecognitionVocabulary() (profilecatalog.RecognitionVocabulary, bool, error) {
	var value profilecatalog.RecognitionVocabulary
	ok, err := s.loadSetting("recognition_vocabulary", &value)
	return value, ok, err
}

func (s *SQLite) SaveRecognitionVocabulary(value profilecatalog.RecognitionVocabulary) error {
	return s.saveSetting("recognition_vocabulary", value)
}

func (s *SQLite) LoadTranslationGlossary() (profilecatalog.TranslationGlossary, bool, error) {
	var value profilecatalog.TranslationGlossary
	ok, err := s.loadSetting("translation_glossary_v2", &value)
	return value, ok, err
}

func (s *SQLite) SaveTranslationGlossary(value profilecatalog.TranslationGlossary) error {
	return s.saveSetting("translation_glossary_v2", value)
}

func (s *SQLite) LoadWebAuth() (auth.Record, bool, error) {
	var value auth.Record
	ok, err := s.loadSetting("web_auth", &value)
	return value, ok, err
}

func (s *SQLite) SaveWebAuth(value auth.Record) error { return s.saveSetting("web_auth", value) }

func (s *SQLite) loadSetting(key string, output any) (bool, error) {
	var payload []byte
	err := s.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&payload)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("load setting %s: %w", key, err)
	}
	if err := json.Unmarshal(payload, output); err != nil {
		return false, fmt.Errorf("decode setting %s: %w", key, err)
	}
	return true, nil
}

func (s *SQLite) saveSetting(key string, value any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO settings (key,value,updated_at) VALUES (?,?,?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value,updated_at=excluded.updated_at`, key, payload, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("save setting %s: %w", key, err)
	}
	return nil
}
