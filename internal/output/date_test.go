package output

import (
	"testing"
	"time"
)

func TestDateFormatterUsesInjectedLocation(t *testing.T) {
	istanbul := mustLocation(t, "Europe/Istanbul")
	now := time.Date(2026, time.January, 15, 12, 0, 0, 0, time.UTC)
	formatter := NewDateFormatter(func() time.Time { return now }, istanbul)
	date := time.Date(2025, time.December, 31, 22, 5, 0, 0, time.UTC)

	if got := formatter.FormatDate(date, false); got != "1 Jan" {
		t.Fatalf("FormatDate = %q", got)
	}
	if got := formatter.FormatDate(date, true); got != "1 Jan 2026" {
		t.Fatalf("FormatDate with year = %q", got)
	}
	if got := formatter.FormatDateLong(date); got != "Thursday, 1 January 2026" {
		t.Fatalf("FormatDateLong = %q", got)
	}
	if got := formatter.FormatTime(date); got != "01:05" {
		t.Fatalf("FormatTime = %q", got)
	}
}

func TestRelativeTimeThresholdsAndRounding(t *testing.T) {
	now := time.Date(2026, time.July, 16, 12, 0, 0, 0, time.UTC)
	formatter := NewDateFormatter(func() time.Time { return now }, time.UTC)
	tests := []struct {
		name string
		diff time.Duration
		want string
	}{
		{"past under minute", -59 * time.Second, "just now"},
		{"future under minute", 59 * time.Second, "just now"},
		{"minute boundary", -time.Minute, "1 minute ago"},
		{"javascript negative half rounding", -90 * time.Second, "1 minute ago"},
		{"positive half rounding", 90 * time.Second, "in 2 minutes"},
		{"hour boundary", -time.Hour, "1 hour ago"},
		{"day boundary", -24 * time.Hour, "yesterday"},
		{"future day", 24 * time.Hour, "tomorrow"},
		{"week boundary", -7 * 24 * time.Hour, "last week"},
		{"future week", 7 * 24 * time.Hour, "next week"},
		{"month boundary", -30 * 24 * time.Hour, "last month"},
		{"future month", 30 * 24 * time.Hour, "next month"},
		{"year boundary", -365 * 24 * time.Hour, "last year"},
		{"future year", 365 * 24 * time.Hour, "next year"},
		{"plural years", -730 * 24 * time.Hour, "2 years ago"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := formatter.FormatRelativeTime(now.Add(test.diff)); got != test.want {
				t.Fatalf("FormatRelativeTime(%s) = %q, want %q", test.diff, got, test.want)
			}
		})
	}
}

func TestDateGroupLabelUsesCalendarDaysAcrossDST(t *testing.T) {
	newYork := mustLocation(t, "America/New_York")
	// The previous local calendar day is only 23 hours away across spring-forward.
	now := time.Date(2026, time.March, 9, 0, 30, 0, 0, newYork)
	formatter := NewDateFormatter(func() time.Time { return now }, newYork)
	tests := []struct {
		date time.Time
		want string
	}{
		{time.Date(2026, time.March, 9, 23, 0, 0, 0, time.UTC), "Today"},
		{time.Date(2026, time.March, 8, 0, 30, 0, 0, newYork), "Yesterday"},
		{time.Date(2026, time.March, 7, 23, 0, 0, 0, newYork), "7 Mar"},
		{time.Date(2025, time.December, 31, 23, 0, 0, 0, newYork), "31 Dec 2025"},
	}
	for _, test := range tests {
		if got := formatter.DateGroupLabel(test.date); got != test.want {
			t.Errorf("DateGroupLabel(%s) = %q, want %q", test.date, got, test.want)
		}
	}
}

func TestRelativeTimeDoesNotSaturateBeyondDurationRange(t *testing.T) {
	now := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	formatter := NewDateFormatter(func() time.Time { return now }, time.UTC)
	if got := formatter.FormatRelativeTime(time.Date(2400, time.January, 1, 0, 0, 0, 0, time.UTC)); got != "in 374 years" {
		t.Fatalf("FormatRelativeTime = %q", got)
	}
}

func TestNewDateFormatterRejectsMissingDependencies(t *testing.T) {
	assertPanic := func(name string, call func()) {
		t.Helper()
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("expected panic")
				}
			}()
			call()
		})
	}
	assertPanic("clock", func() { NewDateFormatter(nil, time.UTC) })
	assertPanic("location", func() { NewDateFormatter(time.Now, nil) })
}

func mustLocation(t *testing.T, name string) *time.Location {
	t.Helper()
	location, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return location
}
