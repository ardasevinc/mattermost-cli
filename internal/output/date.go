package output

import (
	"fmt"
	"math"
	"time"
)

type DateFormatter struct {
	now      func() time.Time
	location *time.Location
}

func NewDateFormatter(now func() time.Time, location *time.Location) DateFormatter {
	if now == nil {
		panic("output: nil clock")
	}
	if location == nil {
		panic("output: nil location")
	}
	return DateFormatter{now: now, location: location}
}

func (f DateFormatter) FormatDate(date time.Time, includeYear bool) string {
	date = date.In(f.location)
	if includeYear {
		return fmt.Sprintf("%d %s %d", date.Day(), date.Format("Jan"), date.Year())
	}
	return fmt.Sprintf("%d %s", date.Day(), date.Format("Jan"))
}

func (f DateFormatter) FormatDateLong(date time.Time) string {
	return date.In(f.location).Format("Monday, 2 January 2006")
}

func (f DateFormatter) FormatTime(date time.Time) string {
	return date.In(f.location).Format("15:04")
}

func (f DateFormatter) FormatRelativeTime(date time.Time) string {
	diffMillis := date.UnixMilli() - f.now().UnixMilli()
	absMillis := diffMillis
	if absMillis < 0 {
		absMillis = -absMillis
	}
	unitMillis := int64(time.Second / time.Millisecond)
	name := ""
	switch {
	case absMillis < int64(time.Minute/time.Millisecond):
		return "just now"
	case absMillis < int64(time.Hour/time.Millisecond):
		unitMillis, name = int64(time.Minute/time.Millisecond), "minute"
	case absMillis < int64((24*time.Hour)/time.Millisecond):
		unitMillis, name = int64(time.Hour/time.Millisecond), "hour"
	case absMillis < int64((7*24*time.Hour)/time.Millisecond):
		unitMillis, name = int64((24*time.Hour)/time.Millisecond), "day"
	case absMillis < int64((30*24*time.Hour)/time.Millisecond):
		unitMillis, name = int64((7*24*time.Hour)/time.Millisecond), "week"
	case absMillis < int64((365*24*time.Hour)/time.Millisecond):
		unitMillis, name = int64((30*24*time.Hour)/time.Millisecond), "month"
	default:
		unitMillis, name = int64((365*24*time.Hour)/time.Millisecond), "year"
	}
	value := int64(math.Floor(float64(diffMillis)/float64(unitMillis) + 0.5)) // JavaScript Math.round
	if name == "day" && value == -1 {
		return "yesterday"
	}
	if name == "day" && value == 1 {
		return "tomorrow"
	}
	if value == -1 && (name == "week" || name == "month" || name == "year") {
		return "last " + name
	}
	if value == 1 && (name == "week" || name == "month" || name == "year") {
		return "next " + name
	}
	label := name
	if value != -1 && value != 1 {
		label += "s"
	}
	if value < 0 {
		return fmt.Sprintf("%d %s ago", -value, label)
	}
	return fmt.Sprintf("in %d %s", value, label)
}

func (f DateFormatter) DateGroupLabel(date time.Time) string {
	now := f.now().In(f.location)
	date = date.In(f.location)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, f.location)
	day := time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, f.location)
	if day.Equal(today) {
		return "Today"
	}
	if day.Equal(today.AddDate(0, 0, -1)) {
		return "Yesterday"
	}
	return f.FormatDate(date, date.Year() != now.Year())
}
