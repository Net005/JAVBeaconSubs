package jobs

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
	_ "time/tzdata"
)

const (
	ScheduleHourly = "hourly"
	ScheduleDaily  = "daily"
	ScheduleWeekly = "weekly"
)

// Schedule is a persistent recurring job template. Job contains the same
// options accepted by POST /api/v1/jobs so scheduled and manual runs share
// discovery, release lookup, profile resolution, and validation.
type Schedule struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Enabled   bool       `json:"enabled"`
	Cadence   string     `json:"cadence"`
	Interval  int        `json:"interval"`
	TimeOfDay string     `json:"time_of_day,omitempty"`
	Weekday   string     `json:"weekday,omitempty"`
	Weekdays  []string   `json:"weekdays,omitempty"`
	Timezone  string     `json:"timezone"`
	Job       Request    `json:"job"`
	NextRunAt *time.Time `json:"next_run_at,omitempty"`
	LastRunAt *time.Time `json:"last_run_at,omitempty"`
	LastJobID string     `json:"last_job_id,omitempty"`
	LastError string     `json:"last_error,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

type SchedulePersistence interface {
	SaveSchedule(*Schedule) error
	LoadSchedules() ([]*Schedule, error)
	DeleteSchedule(string) error
}

func (m *Manager) loadSchedules() error {
	store, ok := m.persistence.(SchedulePersistence)
	if !ok {
		return nil
	}
	items, err := store.LoadSchedules()
	if err != nil {
		return err
	}
	for _, item := range items {
		if item.Interval < 1 {
			item.Interval = 1
		}
		m.schedules[item.ID] = item
	}
	return nil
}

func (m *Manager) ListSchedules() []*Schedule {
	m.mu.RLock()
	result := make([]*Schedule, 0, len(m.schedules))
	for _, item := range m.schedules {
		result = append(result, cloneSchedule(item))
	}
	m.mu.RUnlock()
	sort.Slice(result, func(i, j int) bool {
		if result[i].Name == result[j].Name {
			return result[i].CreatedAt.Before(result[j].CreatedAt)
		}
		return strings.ToLower(result[i].Name) < strings.ToLower(result[j].Name)
	})
	return result
}

func (m *Manager) CreateSchedule(input Schedule) (*Schedule, error) {
	now := time.Now().UTC()
	input.ID = newScheduleID()
	input.CreatedAt = now
	input.UpdatedAt = now
	input.LastRunAt = nil
	input.LastJobID = ""
	input.LastError = ""
	if err := prepareSchedule(&input, now); err != nil {
		return nil, err
	}
	if err := m.saveSchedule(&input); err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.schedules[input.ID] = cloneSchedule(&input)
	m.mu.Unlock()
	return cloneSchedule(&input), nil
}

func (m *Manager) UpdateSchedule(id string, input Schedule) (*Schedule, error) {
	m.mu.RLock()
	existing, ok := m.schedules[id]
	if ok {
		existing = cloneSchedule(existing)
	}
	m.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("schedule not found")
	}
	now := time.Now().UTC()
	input.ID = id
	input.CreatedAt = existing.CreatedAt
	input.UpdatedAt = now
	input.LastRunAt = existing.LastRunAt
	input.LastJobID = existing.LastJobID
	input.LastError = existing.LastError
	if err := prepareSchedule(&input, now); err != nil {
		return nil, err
	}
	if err := m.saveSchedule(&input); err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.schedules[id] = cloneSchedule(&input)
	m.mu.Unlock()
	return cloneSchedule(&input), nil
}

func (m *Manager) DeleteSchedule(id string) error {
	m.mu.RLock()
	_, ok := m.schedules[id]
	m.mu.RUnlock()
	if !ok {
		return fmt.Errorf("schedule not found")
	}
	if store, supported := m.persistence.(SchedulePersistence); supported {
		if err := store.DeleteSchedule(id); err != nil {
			return err
		}
	}
	m.mu.Lock()
	delete(m.schedules, id)
	m.mu.Unlock()
	return nil
}

func (m *Manager) RunScheduleNow(id string) (*Job, error) {
	m.mu.RLock()
	item, ok := m.schedules[id]
	if ok {
		item = cloneSchedule(item)
	}
	m.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("schedule not found")
	}
	return m.executeSchedule(item, time.Now().UTC())
}

func prepareSchedule(item *Schedule, now time.Time) error {
	item.Name = strings.TrimSpace(item.Name)
	item.Cadence = strings.ToLower(strings.TrimSpace(item.Cadence))
	item.TimeOfDay = strings.TrimSpace(item.TimeOfDay)
	item.Weekday = strings.ToLower(strings.TrimSpace(item.Weekday))
	for i := range item.Weekdays {
		item.Weekdays[i] = strings.ToLower(strings.TrimSpace(item.Weekdays[i]))
	}
	item.Timezone = strings.TrimSpace(item.Timezone)
	if item.Name == "" {
		return fmt.Errorf("schedule name is required")
	}
	if len(item.Name) > 120 {
		return fmt.Errorf("schedule name must be 120 characters or fewer")
	}
	if len(item.Job.Inputs) == 0 {
		return fmt.Errorf("schedule must contain at least one input path")
	}
	for i := range item.Job.Inputs {
		item.Job.Inputs[i] = strings.TrimSpace(item.Job.Inputs[i])
		if item.Job.Inputs[i] == "" {
			return fmt.Errorf("schedule input paths cannot be blank")
		}
	}
	if item.Job.ReleaseWithinDays > 0 {
		item.Job.OnlyNewReleases = true
	}
	if item.Job.OnlyNewReleases || item.Job.OnlyWatchlist {
		item.Job.AutoDetectRelease = true
	}
	if err := validateRequest(item.Job); err != nil {
		return err
	}
	if item.Interval < 1 {
		return fmt.Errorf("schedule interval must be at least 1")
	}
	if item.Timezone == "" {
		item.Timezone = "UTC"
	}
	location, err := time.LoadLocation(item.Timezone)
	if err != nil {
		return fmt.Errorf("invalid timezone %q", item.Timezone)
	}
	switch item.Cadence {
	case ScheduleHourly:
		if item.Interval > 168 {
			return fmt.Errorf("hourly interval cannot exceed 168 hours")
		}
		item.TimeOfDay, item.Weekday, item.Weekdays = "", "", nil
	case ScheduleDaily:
		if item.Interval > 365 {
			return fmt.Errorf("daily interval cannot exceed 365 days")
		}
		if _, _, err = parseClock(item.TimeOfDay); err != nil {
			return err
		}
		item.Weekday, item.Weekdays = "", nil
	case ScheduleWeekly:
		if item.Interval > 52 {
			return fmt.Errorf("weekly interval cannot exceed 52 weeks")
		}
		if _, _, err = parseClock(item.TimeOfDay); err != nil {
			return err
		}
		if len(item.Weekdays) == 0 && item.Weekday != "" {
			item.Weekdays = []string{item.Weekday}
		}
		seen := make(map[string]bool, len(item.Weekdays))
		unique := item.Weekdays[:0]
		for _, day := range item.Weekdays {
			if _, err = parseWeekday(day); err != nil {
				return err
			}
			if !seen[day] {
				seen[day] = true
				unique = append(unique, day)
			}
		}
		item.Weekdays = unique
		if len(item.Weekdays) == 0 {
			return fmt.Errorf("weekly schedules require at least one weekday")
		}
		if len(item.Weekdays) > 1 && item.Interval != 1 {
			return fmt.Errorf("multiple weekdays require interval 1")
		}
		item.Weekday = item.Weekdays[0]
	default:
		return fmt.Errorf("cadence must be hourly, daily, or weekly")
	}
	next := firstScheduleRun(*item, now, location)
	if item.Enabled {
		item.NextRunAt = &next
	} else {
		item.NextRunAt = nil
	}
	return nil
}

func firstScheduleRun(item Schedule, after time.Time, location *time.Location) time.Time {
	if item.Cadence == ScheduleHourly {
		return after.Add(time.Duration(item.Interval) * time.Hour).UTC()
	}
	local := after.In(location)
	hour, minute, _ := parseClock(item.TimeOfDay)
	candidate := time.Date(local.Year(), local.Month(), local.Day(), hour, minute, 0, 0, location)
	if item.Cadence == ScheduleWeekly {
		var earliest time.Time
		for _, day := range item.Weekdays {
			weekday, _ := parseWeekday(day)
			option := candidate.AddDate(0, 0, (int(weekday)-int(candidate.Weekday())+7)%7)
			if !option.After(local) {
				option = option.AddDate(0, 0, 7)
			}
			if earliest.IsZero() || option.Before(earliest) {
				earliest = option
			}
		}
		return earliest.UTC()
	}
	if !candidate.After(local) {
		if item.Cadence == ScheduleDaily {
			candidate = candidate.AddDate(0, 0, item.Interval)
		} else {
			candidate = candidate.AddDate(0, 0, 7*item.Interval)
		}
	}
	return candidate.UTC()
}

func nextScheduleRun(item Schedule, scheduledAt, now time.Time) time.Time {
	location, _ := time.LoadLocation(item.Timezone)
	next := scheduledAt.In(location)
	for !next.After(now.In(location)) {
		switch item.Cadence {
		case ScheduleHourly:
			next = next.Add(time.Duration(item.Interval) * time.Hour)
		case ScheduleDaily:
			next = next.AddDate(0, 0, item.Interval)
		case ScheduleWeekly:
			if len(item.Weekdays) > 1 {
				return firstScheduleRun(item, scheduledAt.Add(time.Nanosecond), location)
			}
			next = next.AddDate(0, 0, 7*item.Interval)
		}
	}
	return next.UTC()
}

func parseClock(value string) (int, int, error) {
	parsed, err := time.Parse("15:04", value)
	if err != nil {
		return 0, 0, fmt.Errorf("time_of_day must use HH:MM in 24-hour time")
	}
	return parsed.Hour(), parsed.Minute(), nil
}

func parseWeekday(value string) (time.Weekday, error) {
	weekdays := map[string]time.Weekday{
		"sunday": time.Sunday, "monday": time.Monday, "tuesday": time.Tuesday,
		"wednesday": time.Wednesday, "thursday": time.Thursday, "friday": time.Friday,
		"saturday": time.Saturday,
	}
	weekday, ok := weekdays[value]
	if !ok {
		return 0, fmt.Errorf("weekday must be sunday through saturday")
	}
	return weekday, nil
}

func (m *Manager) runSchedules(ctxDone <-chan struct{}) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctxDone:
			return
		case now := <-ticker.C:
			m.runDueSchedules(now.UTC())
		}
	}
}

func (m *Manager) runDueSchedules(now time.Time) {
	var due []*Schedule
	m.mu.Lock()
	for _, item := range m.schedules {
		if !item.Enabled || item.NextRunAt == nil || item.NextRunAt.After(now) {
			continue
		}
		copy := cloneSchedule(item)
		scheduledAt := *item.NextRunAt
		next := nextScheduleRun(*item, scheduledAt, now)
		item.NextRunAt = &next
		item.UpdatedAt = now
		due = append(due, copy)
		_ = m.saveSchedule(item)
	}
	m.mu.Unlock()
	for _, item := range due {
		go func(schedule *Schedule) {
			if _, err := m.executeSchedule(schedule, now); err != nil {
				m.log.Error("scheduled job failed", "schedule_id", schedule.ID, "schedule_name", schedule.Name, "error", err)
			}
		}(item)
	}
}

func (m *Manager) executeSchedule(item *Schedule, runAt time.Time) (*Job, error) {
	request := item.Job
	request.ScheduleID = item.ID
	request.ScheduleName = item.Name
	job, err := m.Queue(request)
	m.mu.Lock()
	current, ok := m.schedules[item.ID]
	if ok {
		stamp := runAt.UTC()
		current.LastRunAt = &stamp
		current.UpdatedAt = stamp
		if err != nil {
			current.LastError = err.Error()
		} else {
			current.LastError = ""
			current.LastJobID = job.ID
		}
		_ = m.saveSchedule(current)
	}
	m.mu.Unlock()
	return job, err
}

func (m *Manager) saveSchedule(item *Schedule) error {
	store, ok := m.persistence.(SchedulePersistence)
	if !ok {
		return fmt.Errorf("schedule persistence is unavailable")
	}
	return store.SaveSchedule(item)
}

func cloneSchedule(item *Schedule) *Schedule {
	data, _ := json.Marshal(item)
	var result Schedule
	_ = json.Unmarshal(data, &result)
	return &result
}

func newScheduleID() string { return strings.Replace(newID(), "sub_", "sch_", 1) }
