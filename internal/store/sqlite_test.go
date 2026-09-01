package store

import (
	"path/filepath"
	"testing"
	"time"

	"javbeaconsubs/internal/config"
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
