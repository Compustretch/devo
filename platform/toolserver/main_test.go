package main

import (
	"testing"
	"time"
)

func TestDayAtUsesCallerTimezone(t *testing.T) {
	now := time.Date(2026, time.November, 18, 2, 30, 0, 0, time.UTC)
	got, err := dayAt(now, "America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	if got.Date != "2026-11-17" || got.Weekday != "Tuesday" {
		t.Fatalf("today = %s %s, want 2026-11-17 Tuesday", got.Date, got.Weekday)
	}
	if got.Timezone != "America/New_York" {
		t.Fatalf("timezone = %q", got.Timezone)
	}
	if got.Tomorrow.Date != "2026-11-18" || got.Tomorrow.Weekday != "Wednesday" {
		t.Fatalf("tomorrow = %+v", got.Tomorrow)
	}
	if got.Yesterday.Date != "2026-11-16" || got.Yesterday.Weekday != "Monday" {
		t.Fatalf("yesterday = %+v", got.Yesterday)
	}
	if got.Text != "Today is Tuesday" {
		t.Fatalf("text = %q", got.Text)
	}
}

func TestDayAtCalendarArithmeticAcrossDST(t *testing.T) {
	now := time.Date(2026, time.March, 8, 5, 30, 0, 0, time.UTC)
	got, err := dayAt(now, "America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	if got.Date != "2026-03-08" || got.Weekday != "Sunday" {
		t.Fatalf("today = %s %s", got.Date, got.Weekday)
	}
	if got.Yesterday.Date != "2026-03-07" || got.Tomorrow.Date != "2026-03-09" {
		t.Fatalf("date arithmetic crossed DST incorrectly: yesterday=%+v tomorrow=%+v", got.Yesterday, got.Tomorrow)
	}
}

func TestDayAtRejectsMissingOrNonIanaTimezone(t *testing.T) {
	now := time.Date(2026, time.November, 18, 12, 0, 0, 0, time.UTC)
	for _, timezone := range []string{"", "   ", "Local", "Not/A_Real_Zone"} {
		t.Run(timezone, func(t *testing.T) {
			if _, err := dayAt(now, timezone); err == nil {
				t.Fatalf("dayAt accepted invalid timezone %q", timezone)
			}
		})
	}
}
