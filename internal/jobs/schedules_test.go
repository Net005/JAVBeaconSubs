package jobs

import (
	"testing"
	"time"
)

func TestFirstScheduleRunHourlyDailyAndWeekly(t *testing.T) {
	location, err := time.LoadLocation("Europe/Amsterdam")
	if err != nil {
		t.Fatal(err)
	}
	after := time.Date(2026, time.September, 8, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		item Schedule
		want time.Time
	}{
		{"hourly", Schedule{Cadence: ScheduleHourly, Interval: 3}, time.Date(2026, time.September, 8, 15, 0, 0, 0, time.UTC)},
		{"daily fixed time", Schedule{Cadence: ScheduleDaily, Interval: 1, TimeOfDay: "03:00"}, time.Date(2026, time.September, 9, 1, 0, 0, 0, time.UTC)},
		{"selected weekdays", Schedule{Cadence: ScheduleWeekly, Interval: 1, TimeOfDay: "22:30", Weekdays: []string{"monday", "wednesday", "saturday"}}, time.Date(2026, time.September, 9, 20, 30, 0, 0, time.UTC)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := firstScheduleRun(test.item, after, location); !got.Equal(test.want) {
				t.Fatalf("first run = %s, want %s", got, test.want)
			}
		})
	}
}

func TestNextScheduleRunMovesBetweenSelectedWeekdays(t *testing.T) {
	item := Schedule{Cadence: ScheduleWeekly, Interval: 1, TimeOfDay: "22:30", Timezone: "Europe/Amsterdam", Weekdays: []string{"monday", "wednesday", "saturday"}}
	wednesday := time.Date(2026, time.September, 9, 20, 30, 0, 0, time.UTC)
	if got, want := nextScheduleRun(item, wednesday, wednesday), time.Date(2026, time.September, 12, 20, 30, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("next selected weekday = %s, want %s", got, want)
	}
}

func TestPrepareScheduleValidatesAndNormalizesSelectedDays(t *testing.T) {
	now := time.Date(2026, time.September, 8, 12, 0, 0, 0, time.UTC)
	item := Schedule{
		Name: "  Three evenings  ", Enabled: true, Cadence: ScheduleWeekly, Interval: 1,
		TimeOfDay: "22:30", Timezone: "Europe/Amsterdam", Weekdays: []string{"Monday", "wednesday", "monday", "Saturday"},
		Job: Request{Inputs: []string{" /media/jav "}},
	}
	if err := prepareSchedule(&item, now); err != nil {
		t.Fatal(err)
	}
	if item.Name != "Three evenings" || item.Job.Inputs[0] != "/media/jav" {
		t.Fatalf("schedule was not trimmed: %#v", item)
	}
	if len(item.Weekdays) != 3 || item.Weekdays[0] != "monday" || item.Weekdays[2] != "saturday" {
		t.Fatalf("weekdays were not normalized/deduplicated: %#v", item.Weekdays)
	}
	if item.NextRunAt == nil {
		t.Fatal("enabled schedule has no next run")
	}

	item.Weekdays = nil
	item.Weekday = ""
	if err := prepareSchedule(&item, now); err == nil {
		t.Fatal("weekly schedule without a selected day was accepted")
	}
}

func TestPrepareSchedulePreservesLegacySingularWeekday(t *testing.T) {
	item := Schedule{Name: "Legacy", Enabled: true, Cadence: ScheduleWeekly, Interval: 2, TimeOfDay: "04:00", Weekday: "friday", Timezone: "UTC", Job: Request{Inputs: []string{"/media"}}}
	if err := prepareSchedule(&item, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if len(item.Weekdays) != 1 || item.Weekdays[0] != "friday" {
		t.Fatalf("legacy weekday was not retained: %#v", item)
	}
}

func TestPrepareScheduleValidatesNewReleaseFilter(t *testing.T) {
	item := Schedule{Name: "Fresh releases", Enabled: true, Cadence: ScheduleDaily, Interval: 1, TimeOfDay: "03:00", Timezone: "UTC", Job: Request{Inputs: []string{"/media"}, OnlyNewReleases: true, ReleaseStartDate: "2026-09-01"}}
	if err := prepareSchedule(&item, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if !item.Job.AutoDetectRelease {
		t.Fatal("new-release schedule did not enable JAVBeacon auto-detection")
	}
	item.Job.ReleaseStartDate = ""
	if err := prepareSchedule(&item, time.Now().UTC()); err == nil {
		t.Fatal("new-release schedule accepted an empty start date")
	}
}
