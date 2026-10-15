package jobs

import "time"

// Only completed recognition work supplies timing samples. Discovery, queue
// time, existing outputs and failed write preflights cannot predict inference.
type processingTiming struct {
	elapsed time.Duration
	samples int
}

func (t processingTiming) update(job *Job, index, percent int, now time.Time) {
	percent = max(0, min(100, percent))
	job.CurrentFileProgress = percent
	job.ETASeconds, job.EstimatedFinishAt = 0, nil
	if len(job.Files) == 0 {
		job.Progress = 0
		return
	}
	job.Progress = min(99, (index*100+percent)/len(job.Files))
	if t.samples == 0 || t.elapsed <= 0 {
		return
	}
	remainingFiles := float64(len(job.Files)-index) - float64(percent)/100
	remaining := time.Duration(float64(t.elapsed) / float64(t.samples) * remainingFiles)
	if remaining <= 0 {
		return
	}
	job.ETASeconds = max(1, int64(remaining.Round(time.Second)/time.Second))
	finish := now.Add(time.Duration(job.ETASeconds) * time.Second)
	job.EstimatedFinishAt = &finish
}
