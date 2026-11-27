package config

// Validation helpers for the scheduled bandwidth windows (#12).

import "strings"

// isClock reports whether value is a "HH:MM" (or "H:MM") wall clock time.
func isClock(value string) bool {
	value = strings.TrimSpace(value)
	parts := strings.Split(value, ":")
	if len(parts) != 2 {
		return false
	}
	hour, ok := clockNumber(parts[0], 23)
	if !ok {
		return false
	}
	_, ok = clockNumber(parts[1], 59)
	return ok && hour >= 0
}

func clockNumber(value string, max int) (int, bool) {
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
	if n > max {
		return 0, false
	}
	return n, true
}

// isWeekdayName accepts the aliases documented on BandwidthWindow.
func isWeekdayName(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "daily", "everyday", "all", "*",
		"sun", "sunday", "0",
		"mon", "monday", "1",
		"tue", "tuesday", "2",
		"wed", "wednesday", "3",
		"thu", "thursday", "4",
		"fri", "friday", "5",
		"sat", "saturday", "6",
		"weekday", "weekdays", "weekend", "weekends":
		return true
	}
	return false
}
