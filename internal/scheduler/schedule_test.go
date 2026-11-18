package scheduler

import (
	"strings"
	"testing"
	"time"
)

func TestNextRunOnceAndDateBounds(t *testing.T) {
	loc := time.FixedZone("CST", 8*3600)
	rule, err := ValidateRule(ScheduleRule{
		Kind: "once",
		Date: "2026-12-31",
		Time: "23:59",
	})
	if err != nil {
		t.Fatalf("ValidateRule once: %v", err)
	}
	before := time.Date(2026, 12, 31, 23, 58, 0, 0, loc)
	next, ok := NextRun(rule, before, loc)
	if !ok {
		t.Fatal("expected next run before target")
	}
	want := time.Date(2026, 12, 31, 23, 59, 0, 0, loc)
	if !next.Equal(want) {
		t.Fatalf("got %v want %v", next, want)
	}
	if _, ok := NextRun(rule, want, loc); ok {
		t.Fatal("once schedule should not have next run at or after target")
	}

	if _, err := ValidateRule(ScheduleRule{
		Kind:      "once",
		Date:      "2026-12-31",
		Time:      "10:00",
		StartDate: "2027-01-01",
	}); err == nil {
		t.Fatal("expected error when once date precedes startDate")
	}
}

func TestNextRunPresetsAndCustom(t *testing.T) {
	loc := time.FixedZone("CST", 8*3600)

	t.Run("hourly", func(t *testing.T) {
		rule, err := ValidateRule(ScheduleRule{Kind: "hourly", Minute: 15})
		if err != nil {
			t.Fatal(err)
		}
		after := time.Date(2026, 11, 18, 23, 20, 0, 0, loc)
		next, ok := NextRun(rule, after, loc)
		want := time.Date(2026, 11, 19, 0, 15, 0, 0, loc)
		if !ok || !next.Equal(want) {
			t.Fatalf("got (%v, %v), want %v", next, ok, want)
		}
	})

	t.Run("daily_cross_year", func(t *testing.T) {
		rule, err := ValidateRule(ScheduleRule{Kind: "daily", Time: "08:30"})
		if err != nil {
			t.Fatal(err)
		}
		after := time.Date(2026, 12, 31, 9, 0, 0, 0, loc)
		next, ok := NextRun(rule, after, loc)
		want := time.Date(2027, 1, 1, 8, 30, 0, 0, loc)
		if !ok || !next.Equal(want) {
			t.Fatalf("got (%v, %v), want %v", next, ok, want)
		}
	})

	t.Run("weekdays_skips_weekend", func(t *testing.T) {
		rule, err := ValidateRule(ScheduleRule{Kind: "weekdays", Time: "09:00"})
		if err != nil {
			t.Fatal(err)
		}
		// 2026-11-20 is Friday
		after := time.Date(2026, 11, 20, 10, 0, 0, 0, loc)
		next, ok := NextRun(rule, after, loc)
		want := time.Date(2026, 11, 23, 9, 0, 0, 0, loc)
		if !ok || !next.Equal(want) {
			t.Fatalf("got (%v, %v), want %v", next, ok, want)
		}
	})

	t.Run("weekly_specific_days", func(t *testing.T) {
		rule, err := ValidateRule(ScheduleRule{Kind: "weekly", Time: "14:00", Weekdays: []int{3, 1}})
		if err != nil {
			t.Fatal(err)
		}
		// 2026-11-18 is Wednesday
		after := time.Date(2026, 11, 18, 15, 0, 0, 0, loc)
		next, ok := NextRun(rule, after, loc)
		want := time.Date(2026, 11, 23, 14, 0, 0, 0, loc)
		if !ok || !next.Equal(want) {
			t.Fatalf("got (%v, %v), want %v", next, ok, want)
		}
	})

	t.Run("monthly_31_and_leap_feb_and_last_day", func(t *testing.T) {
		rule31, err := ValidateRule(ScheduleRule{Kind: "monthly", Time: "10:00", DaysOfMonth: []int{31}})
		if err != nil {
			t.Fatal(err)
		}
		// After Jan 31, next 31st is March 31 (Feb skipped)
		afterJan := time.Date(2026, 1, 31, 12, 0, 0, 0, loc)
		next, ok := NextRun(rule31, afterJan, loc)
		wantMar := time.Date(2026, 3, 31, 10, 0, 0, 0, loc)
		if !ok || !next.Equal(wantMar) {
			t.Fatalf("monthly 31 got (%v, %v), want %v", next, ok, wantMar)
		}

		ruleLast, err := ValidateRule(ScheduleRule{Kind: "monthly", Time: "18:00", LastDay: true})
		if err != nil {
			t.Fatal(err)
		}
		// Leap year 2024 Feb -> Feb 29
		afterFeb2024 := time.Date(2024, 2, 1, 0, 0, 0, 0, loc)
		nextLeap, ok := NextRun(ruleLast, afterFeb2024, loc)
		wantLeap := time.Date(2024, 2, 29, 18, 0, 0, 0, loc)
		if !ok || !nextLeap.Equal(wantLeap) {
			t.Fatalf("monthly lastDay leap got (%v, %v), want %v", nextLeap, ok, wantLeap)
		}
		// Non-leap 2026 Feb -> Feb 28
		afterFeb2026 := time.Date(2026, 2, 1, 0, 0, 0, 0, loc)
		nextNonLeap, ok := NextRun(ruleLast, afterFeb2026, loc)
		wantNonLeap := time.Date(2026, 2, 28, 18, 0, 0, 0, loc)
		if !ok || !nextNonLeap.Equal(wantNonLeap) {
			t.Fatalf("monthly lastDay non-leap got (%v, %v), want %v", nextNonLeap, ok, wantNonLeap)
		}
	})

	t.Run("interval_normal_vs_test_mode", func(t *testing.T) {
		if _, err := ValidateRule(ScheduleRule{Kind: "interval", IntervalValue: 1, IntervalUnit: "minutes"}); err == nil {
			t.Fatal("expected 1-minute interval without testMode to fail")
		}
		rule, err := ValidateRule(ScheduleRule{Kind: "interval", IntervalValue: 1, IntervalUnit: "minutes", TestMode: true})
		if err != nil {
			t.Fatalf("expected 1-minute interval with testMode to succeed: %v", err)
		}
		after := time.Date(2026, 11, 18, 13, 40, 12, 0, loc)
		next, ok := NextRun(rule, after, loc)
		want := time.Date(2026, 11, 18, 13, 41, 0, 0, loc)
		if !ok || !next.Equal(want) {
			t.Fatalf("interval 1m got (%v, %v), want %v", next, ok, want)
		}
	})

	t.Run("times_with_weekdays_and_range", func(t *testing.T) {
		rule, err := ValidateRule(ScheduleRule{
			Kind:      "times",
			Times:     []string{"18:00", "09:00"},
			Weekdays:  []int{1, 3},
			StartDate: "2026-11-20",
			EndDate:   "2026-11-24",
		})
		if err != nil {
			t.Fatal(err)
		}
		after := time.Date(2026, 11, 18, 8, 0, 0, 0, loc)
		next, ok := NextRun(rule, after, loc)
		// 2026-11-23 is Monday
		want := time.Date(2026, 11, 23, 9, 0, 0, 0, loc)
		if !ok || !next.Equal(want) {
			t.Fatalf("times got (%v, %v), want %v", next, ok, want)
		}
		afterEnd := time.Date(2026, 11, 23, 18, 0, 0, 0, loc)
		if _, ok := NextRun(rule, afterEnd, loc); ok {
			t.Fatal("expected no run after endDate")
		}
	})
}

func TestFormatScheduleSummary(t *testing.T) {
	s := FormatScheduleSummary(ScheduleRule{Kind: "weekdays", Time: "09:00"})
	if s != "每个工作日 09:00" {
		t.Fatalf("got %q", s)
	}
	s2 := FormatScheduleSummary(ScheduleRule{Kind: "interval", IntervalValue: 1, IntervalUnit: "minutes", TestMode: true})
	if !strings.Contains(s2, "每 1 分钟") || !strings.Contains(s2, "测试模式") {
		t.Fatalf("got %q", s2)
	}
}
