package scheduler

// #12: scheduled bandwidth windows.

import (
	"testing"
	"time"

	"github.com/asdfgh129/gobitmule/internal/config"
)

func TestEffectiveBandwidthWithoutSchedule(t *testing.T) {
	cfg := config.GlobalConfig{MaxDownloadRate: "8MiB/s", MaxUploadRate: "2MiB/s"}
	down, up := EffectiveBandwidth(cfg, time.Date(2026, 11, 27, 12, 0, 0, 0, time.Local))
	if down != "8MiB/s" || up != "2MiB/s" {
		t.Fatalf("rates = %s/%s", down, up)
	}
}

func TestEffectiveBandwidthWeekendWindow(t *testing.T) {
	cfg := config.GlobalConfig{
		MaxDownloadRate: "8MiB/s",
		MaxUploadRate:   "2MiB/s",
		BandwidthSchedule: []config.BandwidthWindow{{
			Days:            []string{"sat", "sun"},
			Start:           "09:00",
			End:             "23:00",
			MaxDownloadRate: "20MiB/s",
			MaxUploadRate:   "5MiB/s",
		}},
	}
	// Friday 2026-11-27 15:00 -> no match.
	down, _ := EffectiveBandwidth(cfg, time.Date(2026, 11, 27, 15, 0, 0, 0, time.Local))
	if down != "8MiB/s" {
		t.Fatalf("friday rate = %s, want the base rate", down)
	}
	// Saturday 2026-11-28 10:00 -> weekend window.
	down, up := EffectiveBandwidth(cfg, time.Date(2026, 11, 28, 10, 0, 0, 0, time.Local))
	if down != "20MiB/s" || up != "5MiB/s" {
		t.Fatalf("saturday rates = %s/%s", down, up)
	}
	// Saturday 23:30 -> outside the window.
	down, _ = EffectiveBandwidth(cfg, time.Date(2026, 11, 28, 23, 30, 0, 0, time.Local))
	if down != "8MiB/s" {
		t.Fatalf("late saturday rate = %s", down)
	}
}

func TestEffectiveBandwidthOvernightWindow(t *testing.T) {
	cfg := config.GlobalConfig{
		MaxDownloadRate: "8MiB/s",
		MaxUploadRate:   "2MiB/s",
		BandwidthSchedule: []config.BandwidthWindow{{
			Days:            []string{"fri"},
			Start:           "23:00",
			End:             "07:00",
			MaxDownloadRate: "1MiB/s",
			MaxUploadRate:   "256KiB/s",
		}},
	}
	// Friday 23:30 is inside (start day matches).
	down, up := EffectiveBandwidth(cfg, time.Date(2026, 11, 27, 23, 30, 0, 0, time.Local))
	if down != "1MiB/s" || up != "256KiB/s" {
		t.Fatalf("friday night rates = %s/%s", down, up)
	}
	// Saturday 02:00 still belongs to friday's window.
	down, _ = EffectiveBandwidth(cfg, time.Date(2026, 11, 28, 2, 0, 0, 0, time.Local))
	if down != "1MiB/s" {
		t.Fatalf("saturday early rate = %s", down)
	}
	// Saturday 08:00 is outside.
	down, _ = EffectiveBandwidth(cfg, time.Date(2026, 11, 28, 8, 0, 0, 0, time.Local))
	if down != "8MiB/s" {
		t.Fatalf("saturday morning rate = %s", down)
	}
	// Saturday 23:30 is outside: the window belongs to friday.
	down, _ = EffectiveBandwidth(cfg, time.Date(2026, 11, 28, 23, 30, 0, 0, time.Local))
	if down != "8MiB/s" {
		t.Fatalf("saturday night rate = %s", down)
	}
}

func TestLimiterApplyAtUsesWindow(t *testing.T) {
	cfg := config.GlobalConfig{
		MaxDownloadRate: "0",
		MaxUploadRate:   "0",
		BandwidthSchedule: []config.BandwidthWindow{{
			Days:            []string{"daily"},
			Start:           "00:00",
			End:             "23:59",
			MaxDownloadRate: "1KiB/s",
			MaxUploadRate:   "0",
		}},
	}
	limiter := NewLimiter(config.GlobalConfig{MaxDownloadRate: "0", MaxUploadRate: "0"})
	limiter.ApplyAt(cfg, time.Now())
	if got := limiter.GlobalRate(); got != 1024 {
		t.Fatalf("global rate = %d, want 1024", got)
	}
}
