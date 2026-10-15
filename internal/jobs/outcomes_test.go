package jobs

import (
	"fmt"
	"testing"

	"javbeaconsubs/internal/engine"
)

func TestSummaryRecentOutcomesAreBoundedAndKeepPerFileStatus(t *testing.T) {
	job := &Job{}
	for i := 0; i < 10; i++ {
		path := fmt.Sprintf("/media/example-%02d.mp4", i)
		job.Files = append(job.Files, path)
		switch i % 3 {
		case 0:
			job.FileFailures = append(job.FileFailures, FileFailure{Path: path, Error: "Synthetic failure"})
		case 1:
			job.Results = append(job.Results, engine.Result{Input: path})
		case 2:
			job.Results = append(job.Results, engine.Result{Input: path, Skipped: true})
		}
	}
	summary := Summary(job)
	if len(summary.RecentFileResults) != 6 || summary.RecentFileResults[0].Filename != "example-09.mp4" {
		t.Fatal("recent results must be bounded and newest first")
	}
	for i, want := range []string{"failed", "skipped", "completed", "failed", "skipped", "completed"} {
		if summary.RecentFileResults[i].Status != want {
			t.Fatalf("file %d: want %s", i, want)
		}
	}
	if summary.Results != nil || summary.FileFailures != nil || len(job.FileFailures) != 4 {
		t.Fatal("summary must preserve durable records without copying their bodies")
	}
}
