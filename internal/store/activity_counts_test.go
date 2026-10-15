package store

import (
	"path/filepath"
	"testing"

	"javbeaconsubs/internal/engine"
)

func TestActivityMigrationRefreshesLegacyCountsWithoutChangingJobs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	job := activityFixture()
	job.Results = []engine.Result{{Skipped: true}, {Skipped: true}}
	if err := db.Save(job); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.Exec(`UPDATE job_activity SET summary_payload=json_remove(summary_payload,'$.recent_file_results')`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	items, _, err := db.ListSummaryPage(1, 25, "", "")
	if err != nil || len(items) != 1 || items[0].GeneratedCount != 0 || items[0].SkippedCount != 2 {
		t.Fatalf("historical counts not refreshed: %v", err)
	}
	full, ok, err := db.GetJob(job.ID)
	if err != nil || !ok || full.Status != "running" || len(full.Results) != 2 {
		t.Fatal("migration changed durable job state")
	}
}
