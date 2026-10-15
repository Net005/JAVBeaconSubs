package jobs

import (
	"path/filepath"
	"strings"
)

type ActivityFile struct {
	Path     string              `json:"path"`
	Metadata FileReleaseMetadata `json:"metadata"`
}

type FileOutcome struct {
	Path     string `json:"path"`
	Filename string `json:"filename"`
	Status   string `json:"status"`
}

// Summary is a bounded activity record. Full discovery, outputs and release
// stories remain available from the individual job endpoint.
func Summary(job *Job) *Job {
	item := *job
	item.FileCount = len(job.Files)
	item.CompletedCount = len(job.Results)
	item.GeneratedCount, item.SkippedCount = 0, 0
	for _, result := range job.Results {
		if result.Skipped {
			item.SkippedCount++
		} else {
			item.GeneratedCount++
		}
	}
	item.DiscoveredCount = max(job.DiscoveredCount, len(job.ScanResults))
	item.RecentFileResults = nil
	outcomes := make(map[string]string, len(job.Results)+len(job.FileFailures))
	for _, failure := range job.FileFailures {
		outcomes[failure.Path] = "failed"
	}
	for _, result := range job.Results {
		if result.Skipped {
			outcomes[result.Input] = "skipped"
		} else {
			outcomes[result.Input] = "completed"
		}
	}
	for i := len(job.Files) - 1; i >= 0 && len(item.RecentFileResults) < 6; i-- {
		path := job.Files[i]
		if status, ok := outcomes[path]; ok {
			item.RecentFileResults = append(item.RecentFileResults, FileOutcome{Path: path, Filename: filepath.Base(path), Status: status})
		}
	}
	item.FailureCount = len(job.FileFailures)
	item.Files = nil
	item.Inputs = nil
	item.Results = nil
	item.ScanResults = nil
	item.FileFailures = nil
	item.FileSettings = nil
	item.ReleaseStory = ""
	item.FileReleaseMetadata = nil
	item.NextFiles = nil
	if metadata, ok := job.FileReleaseMetadata[job.CurrentPath]; ok {
		metadata.ReleaseStory = ""
		item.FileReleaseMetadata = map[string]FileReleaseMetadata{job.CurrentPath: metadata}
	}
	if job.Status == "queued" || job.Status == "running" {
		start := 0
		if job.Status == "running" {
			start = job.CurrentFileNumber
			for i, path := range job.Files {
				if path == job.CurrentPath {
					start = i + 1
					break
				}
			}
		}
		if start < 0 {
			start = 0
		}
		for i := start; i < len(job.Files) && len(item.NextFiles) < 4; i++ {
			metadata := job.FileReleaseMetadata[job.Files[i]]
			metadata.ReleaseStory = ""
			item.NextFiles = append(item.NextFiles, ActivityFile{Path: job.Files[i], Metadata: metadata})
		}
	}
	return &item
}

func SearchText(job *Job) string {
	parts := []string{job.ID, job.ExternalID, job.ReleaseTitle, job.ReleaseExternalID, job.CurrentFile, job.CurrentPath}
	parts = append(parts, job.Files...)
	parts = append(parts, job.Inputs...)
	for _, metadata := range job.FileReleaseMetadata {
		parts = append(parts, metadata.ReleaseTitle, metadata.ReleaseExternalID)
	}
	return strings.ToLower(strings.ReplaceAll(strings.Join(parts, " "), "\\", "/"))
}

func (m *Manager) ListSummaryPage(page, size int, filter, group string) (Page, error) {
	if page < 1 {
		page = 1
	}
	if size != 25 && size != 50 && size != 100 {
		size = 25
	}
	if group != "active" && group != "completed" && group != "failed" {
		group = ""
	}
	if store, ok := m.persistence.(interface {
		ListSummaryPage(int, int, string, string) ([]*Job, int, error)
	}); ok {
		items, total, err := store.ListSummaryPage(page, size, filter, group)
		return Page{Jobs: items, Total: total, Page: page, PageSize: size, Pages: (total + size - 1) / size}, err
	}
	result, err := m.ListPage(page, size, filter, group)
	for i, job := range result.Jobs {
		result.Jobs[i] = Summary(job)
	}
	return result, err
}
