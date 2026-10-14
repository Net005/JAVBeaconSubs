package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"javbeaconsubs/internal/engine"
	"javbeaconsubs/internal/jobs"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func activityFixture() *jobs.Job {
	job := &jobs.Job{ID: "synthetic-batch", Status: "running", CurrentPath: "/media/example-00.mp4", CreatedAt: time.Now().UTC(), FileReleaseMetadata: map[string]jobs.FileReleaseMetadata{}}
	for i := 0; i < 40; i++ {
		path := fmt.Sprintf("/media/example-%02d.mp4", i)
		job.Files = append(job.Files, path)
		job.ScanResults = append(job.ScanResults, jobs.ScanResult{Path: path, Included: true})
		job.FileReleaseMetadata[path] = jobs.FileReleaseMetadata{ReleaseTitle: fmt.Sprintf("Example release %02d", i), ReleaseStory: strings.Repeat("Synthetic story. ", 1000), ReleaseImageURL: "https://example.com/cover.jpg"}
	}
	job.Results = []engine.Result{{Input: "/media/example-01.mp4"}}
	return job
}

func TestActivitySummaryPreservesFullJobAndSearch(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	job := activityFixture()
	if err := db.Save(job); err != nil {
		t.Fatal(err)
	}
	items, total, err := db.ListSummaryPage(1, 25, "Example release 39", "active")
	if err != nil || total != 1 || len(items) != 1 {
		t.Fatalf("summary search: total=%d items=%d error=%v", total, len(items), err)
	}
	summary := items[0]
	if summary.FileCount != 40 || summary.CompletedCount != 1 || summary.DiscoveredCount != 40 || len(summary.Files) != 0 || len(summary.ScanResults) != 0 || len(summary.Results) != 0 || len(summary.FileReleaseMetadata) != 1 {
		t.Fatalf("incorrect bounded summary: %#v", summary)
	}
	if metadata := summary.FileReleaseMetadata[job.CurrentPath]; metadata.ReleaseStory != "" || metadata.ReleaseImageURL == "" {
		t.Fatal("current release summary lost cover or retained story")
	}
	full, ok, err := db.GetJob(job.ID)
	if err != nil || !ok || len(full.Files) != 40 || len(full.FileReleaseMetadata) != 40 || full.FileReleaseMetadata[job.CurrentPath].ReleaseStory == "" {
		t.Fatal("full job was changed")
	}
	fullBytes, _ := json.Marshal(full)
	summaryBytes, _ := json.Marshal(summary)
	if len(summaryBytes)*20 > len(fullBytes) {
		t.Fatalf("summary too large: %d full, %d summary", len(fullBytes), len(summaryBytes))
	}
	job.Status = "cancelled"
	if err := db.Save(job); err != nil {
		t.Fatal(err)
	}
	if _, total, err := db.ListSummaryPage(1, 25, "", "active"); err != nil || total != 0 {
		t.Fatal("summary status not updated atomically")
	}
}

func TestActivityMigrationBackfillsLegacyJobs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE jobs(id TEXT PRIMARY KEY,status TEXT NOT NULL,external_id TEXT NOT NULL DEFAULT '',payload BLOB NOT NULL,callback_url TEXT NOT NULL DEFAULT '',overwrite_existing INTEGER NOT NULL DEFAULT 0,created_at TEXT NOT NULL,updated_at TEXT NOT NULL)`)
	if err != nil {
		t.Fatal(err)
	}
	job := activityFixture()
	payload, _ := json.Marshal(job)
	_, err = db.Exec(`INSERT INTO jobs(id,status,payload,created_at,updated_at) VALUES(?,?,?,?,?)`, job.ID, job.Status, payload, "2026-01-01", "2026-01-02")
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	items, total, err := store.ListSummaryPage(1, 25, "*example-39*", "active")
	if err != nil || total != 1 || len(items) != 1 || items[0].FileCount != 40 {
		t.Fatalf("legacy migration failed: total=%d error=%v", total, err)
	}
	var updated string
	if err := store.db.QueryRow(`SELECT updated_at FROM jobs WHERE id=?`, job.ID).Scan(&updated); err != nil || updated != "2026-01-02" {
		t.Fatal("migration changed durable timestamps")
	}
	store.Close()
	store, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, total, err := store.ListSummaryPage(1, 25, "Example release 39", "active"); err != nil || total != 1 {
		t.Fatal("migration is not restart safe")
	}
}

func TestActivitySaveRollsBackFullJobOnSummaryFailure(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	job := activityFixture()
	if err := db.Save(job); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.Exec(`CREATE TRIGGER reject_activity BEFORE INSERT ON job_activity BEGIN SELECT RAISE(ABORT,'synthetic failure'); END`); err != nil {
		t.Fatal(err)
	}
	job.Status = "cancelled"
	if err := db.Save(job); err == nil {
		t.Fatal("expected activity write failure")
	}
	stored, ok, err := db.GetJob(job.ID)
	if err != nil || !ok || stored.Status != "running" {
		t.Fatal("failed summary write changed durable job state")
	}
}

func BenchmarkActivitySummaryPage(b *testing.B) {
	db, err := Open(filepath.Join(b.TempDir(), "bench.db"))
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()
	job := activityFixture()
	for i := 0; i < 200; i++ {
		job.ID = fmt.Sprintf("batch-%03d", i)
		if err := db.Save(job); err != nil {
			b.Fatal(err)
		}
	}
	for _, view := range []string{"full", "summary"} {
		b.Run(view, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if view == "full" {
					_, _, err = db.ListPage(1, 25, "", "active")
				} else {
					_, _, err = db.ListSummaryPage(1, 25, "", "active")
				}
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
