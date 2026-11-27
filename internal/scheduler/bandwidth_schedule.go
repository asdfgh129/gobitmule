package scheduler

// #12: time based bandwidth schedule, the equivalent of eMule's
// CScheduler/CBandwidthActivation.
//
// A window says "between 22:00 and 06:00 on these days, cap the rates at X".
// Windows are evaluated in order and the first match wins; when nothing
// matches the global rates apply. Overnight windows (start > end) belong to the
// day they start on, so 22:00-06:00 selected for "fri" covers saturday
// 00:00-06:00.

import (
	"strings"
	"time"

	"github.com/asdfgh129/gobitmule/internal/config"
)

// EffectiveBandwidth lives in limiter.go: guard 4 allows only that file to read
// the global rate fields, and a window's caps are rate config too.

// windowMatches reports whether now falls inside the window's day/time span.
func windowMatches(window config.BandwidthWindow, now time.Time) bool {
	start, ok := parseClock(window.Start)
	if !ok {
		return false
	}
	end, ok := parseClock(window.End)
	if !ok {
		return false
	}
	minute := now.Hour()*60 + now.Minute()
	overnight := end <= start
	if !overnight {
		return minute >= start && minute < end && dayMatches(window.Days, now.Weekday())
	}
	// Overnight: before the end we are still in the previous day's window.
	if minute < end {
		return dayMatches(window.Days, previousDay(now.Weekday()))
	}
	return minute >= start && dayMatches(window.Days, now.Weekday())
}

// parseClock parses "HH:MM" (or "H:MM") into minutes after midnight.
func parseClock(value string) (int, bool) {
	value = strings.TrimSpace(value)
	parts := strings.Split(value, ":")
	if len(parts) != 2 {
		return 0, false
	}
	hour, ok := parseSmallInt(parts[0])
	if !ok || hour > 23 {
		return 0, false
	}
	minute, ok := parseSmallInt(parts[1])
	if !ok || minute > 59 {
		return 0, false
	}
	return hour*60 + minute, true
}

func parseSmallInt(value string) (int, bool) {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 2 {
		return 0, false
	}
	n := 0
	for _, r := range value {
		if r < '0' || r > '9' {
			return 0, false
		}
		n = n*10 + int(r-'0')
	}
	return n, true
}

// dayMatches accepts English short/long names, "daily"/"everyday" and numeric
// weekdays (0 = Sunday, matching time.Weekday).
func dayMatches(days []string, day time.Weekday) bool {
	if len(days) == 0 {
		return true
	}
	for _, entry := range days {
		entry = strings.ToLower(strings.TrimSpace(entry))
		switch entry {
		case "", "daily", "everyday", "all", "*":
			return true
		case "sun", "sunday", "0":
			if day == time.Sunday {
				return true
			}
		case "mon", "monday", "1":
			if day == time.Monday {
				return true
			}
		case "tue", "tuesday", "2":
			if day == time.Tuesday {
				return true
			}
		case "wed", "wednesday", "3":
			if day == time.Wednesday {
				return true
			}
		case "thu", "thursday", "4":
			if day == time.Thursday {
				return true
			}
		case "fri", "friday", "5":
			if day == time.Friday {
				return true
			}
		case "sat", "saturday", "6":
			if day == time.Saturday {
				return true
			}
		case "weekday", "weekdays":
			if day >= time.Monday && day <= time.Friday {
				return true
			}
		case "weekend", "weekends":
			if day == time.Saturday || day == time.Sunday {
				return true
			}
		}
	}
	return false
}

func previousDay(day time.Weekday) time.Weekday {
	if day == time.Sunday {
		return time.Saturday
	}
	return day - 1
}
