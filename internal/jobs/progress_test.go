package jobs

import (
	"testing"
	"time"

	"javbeaconsubs/internal/engine"
)

func TestProcessingProgressDoesNotCountPreparationOrSkippedTime(t *testing.T) {
	now := time.Now()
	job := &Job{Files: make([]string, 100), Progress: 50, ETASeconds: 999}
	var timing processingTiming
	timing.update(job, 20, 15, now)
	if job.Progress != 20 || job.CurrentFileProgress != 15 || job.ETASeconds != 0 || job.EstimatedFinishAt != nil {
		t.Fatalf("first real file after skips: progress=%d file=%d eta=%d", job.Progress, job.CurrentFileProgress, job.ETASeconds)
	}
	timing.elapsed, timing.samples = 10*time.Minute, 1
	timing.update(job, 21, 50, now)
	if job.Progress != 21 || job.ETASeconds != 47100 || !job.EstimatedFinishAt.Equal(now.Add(785*time.Minute)) {
		t.Fatalf("ETA should use completed processing, got %d seconds", job.ETASeconds)
	}
}

func TestProcessingProgressBoundsAndNoPrematureCompletion(t *testing.T) {
	job := &Job{Files: []string{"example"}}
	var timing processingTiming
	timing.update(job, 0, 500, time.Now())
	if job.Progress != 99 || job.CurrentFileProgress != 100 {
		t.Fatal("completion must wait for final writes")
	}
	timing.update(job, 0, -10, time.Now())
	if job.Progress != 0 || job.CurrentFileProgress != 0 {
		t.Fatal("negative progress")
	}
}

func TestSummarySeparatesGeneratedSkippedAndFailed(t *testing.T) {
	job := &Job{Results: []engine.Result{{}, {Skipped: true}, {Skipped: true}}, FileFailures: []FileFailure{{}}}
	summary := Summary(job)
	if summary.CompletedCount != 3 || summary.GeneratedCount != 1 || summary.SkippedCount != 2 || summary.FailureCount != 1 {
		t.Fatal("activity counts misrepresent skipped output as generated")
	}
}
