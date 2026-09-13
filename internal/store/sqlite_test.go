package store

import (
	"path/filepath"
	"testing"
	"time"

	"javbeaconsubs/internal/config"
	"javbeaconsubs/internal/engine"
	"javbeaconsubs/internal/jobs"
)

func TestSQLiteRoundTrip(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	created := time.Now().UTC().Truncate(time.Microsecond)
	want := &jobs.Job{
		ID: "sub_test", ExternalID: "movie-7", Status: "complete", Progress: 100,
		Inputs: []string{"/media/movie.mkv"}, Files: []string{"/media/movie.mkv"},
		CallbackURL: "http://javbeacon/callback", Overwrite: true, KeepJapanese: true, CreatedAt: created,
		Results: []engine.Result{{Input: "/media/movie.mkv", ReleaseContext: &engine.ReleaseContextDiagnostics{
			ReleaseExternalID: "SPSF-50", TitlePresent: true, StoryPresent: true,
			TitleSource: "javbeacon", StorySource: "javbeacon", LookupMethod: "external_release_id",
			LookupMatched: true, UsedForTranslation: true,
		}}},
	}
	if err := database.Save(want); err != nil {
		t.Fatal(err)
	}
	loaded, err := database.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 1 {
		t.Fatalf("loaded %d jobs, want 1", len(loaded))
	}
	got := loaded[0]
	if got.ID != want.ID || got.ExternalID != want.ExternalID || got.CallbackURL != want.CallbackURL || !got.Overwrite || !got.KeepJapanese {
		t.Fatalf("round trip mismatch: %#v", got)
	}
	if len(got.Results) != 1 || got.Results[0].ReleaseContext == nil || !got.Results[0].ReleaseContext.UsedForTranslation || got.Results[0].ReleaseContext.LookupMethod != "external_release_id" {
		t.Fatalf("release context diagnostics did not persist: %#v", got.Results)
	}
}

func TestTranslationSettingsRoundTrip(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "settings.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if _, ok, err := database.LoadTranslation(); err != nil || ok {
		t.Fatalf("unexpected initial settings: ok=%v err=%v", ok, err)
	}
	want := config.TranslationConfig{Mode: "contextual", BaseURL: "http://llm/v1", APIKey: "secret", Model: "qwen", BatchSize: 16, TimeoutSec: 90, Glossary: "A=A"}
	if err := database.SaveTranslation(want); err != nil {
		t.Fatal(err)
	}
	got, ok, err := database.LoadTranslation()
	if err != nil || !ok {
		t.Fatalf("load settings: ok=%v err=%v", ok, err)
	}
	if got != want {
		t.Fatalf("settings mismatch: %#v", got)
	}
}

func TestScheduleRoundTripAndDelete(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "schedules.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	next := time.Now().UTC().Add(time.Hour).Truncate(time.Microsecond)
	want := &jobs.Schedule{
		ID: "sch_test", Name: "Night scan", Enabled: true, Cadence: jobs.ScheduleWeekly,
		Interval: 1, TimeOfDay: "22:30", Weekdays: []string{"monday", "friday"}, Timezone: "Europe/Amsterdam",
		Job: jobs.Request{Inputs: []string{"/media/jav"}, Recursive: true, AutoDetectRelease: true}, NextRunAt: &next,
	}
	if err := database.SaveSchedule(want); err != nil {
		t.Fatal(err)
	}
	loaded, err := database.LoadSchedules()
	if err != nil || len(loaded) != 1 {
		t.Fatalf("load schedules: count=%d err=%v", len(loaded), err)
	}
	if got := loaded[0]; got.ID != want.ID || len(got.Weekdays) != 2 || !got.Job.Recursive || got.NextRunAt == nil || !got.NextRunAt.Equal(next) {
		t.Fatalf("schedule round trip mismatch: %#v", got)
	}
	if err := database.DeleteSchedule(want.ID); err != nil {
		t.Fatal(err)
	}
	if loaded, err = database.LoadSchedules(); err != nil || len(loaded) != 0 {
		t.Fatalf("schedule was not deleted: count=%d err=%v", len(loaded), err)
	}
}

func TestJobPaginationAndWildcardFiltering(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	for index, item := range []struct{ id, name, path string }{
		{"one", "SPSF-57", `D:\P2P\JH\SPSF-57.mp4`},
		{"two", "START-611", "/mnt/data/jav/START-611.mkv"},
		{"three", "Other", "/mnt/data/other/movie.avi"},
	} {
		if err := database.Save(&jobs.Job{ID: item.id, ExternalID: item.name, Status: "complete", Files: []string{item.path}, CreatedAt: time.Now().UTC().Add(time.Duration(index) * time.Second)}); err != nil {
			t.Fatal(err)
		}
	}
	for _, filter := range []string{"*spsf*", `*\p2p\jh\*`, "START", "*.AVI"} {
		items, total, err := database.ListPage(1, 25, filter, "")
		if err != nil || total != 1 || len(items) != 1 {
			t.Fatalf("filter %q: total=%d jobs=%d err=%v", filter, total, len(items), err)
		}
	}
	items, total, err := database.ListPage(2, 25, "", "")
	if err != nil || total != 3 || len(items) != 0 {
		t.Fatalf("page 2: total=%d jobs=%d err=%v", total, len(items), err)
	}
	items, total, err = database.ListPage(1, 25, `%' OR 1=1 --`, "")
	if err != nil || total != 0 || len(items) != 0 {
		t.Fatalf("injection-like filter: total=%d jobs=%d err=%v", total, len(items), err)
	}
	items, total, err = database.ListPage(1, 25, "", "completed")
	if err != nil || total != 3 || len(items) != 3 {
		t.Fatalf("completed status page: total=%d jobs=%d err=%v", total, len(items), err)
	}
	items, total, err = database.ListPage(1, 25, "", "active")
	if err != nil || total != 0 || len(items) != 0 {
		t.Fatalf("active status page: total=%d jobs=%d err=%v", total, len(items), err)
	}
}

func TestJobPaginationUsesProcessingOrder(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	base := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	for _, item := range []*jobs.Job{
		{ID: "complete", Status: "complete", CreatedAt: base.Add(4 * time.Minute)},
		{ID: "queued-new", Status: "queued", CreatedAt: base.Add(3 * time.Minute)},
		{ID: "running", Status: "running", CreatedAt: base.Add(time.Minute)},
		{ID: "queued-old", Status: "queued", CreatedAt: base.Add(2 * time.Minute)},
	} {
		if err := database.Save(item); err != nil {
			t.Fatal(err)
		}
	}
	page, _, err := database.ListPage(1, 25, "", "")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"running", "queued-old", "queued-new", "complete"}
	for index, id := range want {
		if page[index].ID != id {
			t.Fatalf("job %d = %q, want %q", index, page[index].ID, id)
		}
	}
}
