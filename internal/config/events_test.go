package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// docs/06 §7: default retention is 7 days / 100k rows.
func TestEventsDefaults(t *testing.T) {
	cfg := Defaults()
	if cfg.Events.RetentionDays != 7 || cfg.Events.MaxRows != 100_000 {
		t.Fatalf("events defaults = %+v, want 7 days / 100000 rows", cfg.Events)
	}
	if d := cfg.Events.EffectivePruneInterval(); d != time.Hour {
		t.Fatalf("prune interval = %v, want 1h", d)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("default config must be valid: %v", err)
	}
}

func TestEventsCutoffMillis(t *testing.T) {
	now := time.UnixMilli(1_700_000_000_000)
	if got := (EventsConfig{RetentionDays: 0}).CutoffMillis(now); got != 0 {
		t.Fatalf("no age limit must yield cutoff 0, got %d", got)
	}
	want := now.Add(-7 * 24 * time.Hour).UnixMilli()
	if got := (EventsConfig{RetentionDays: 7}).CutoffMillis(now); got != want {
		t.Fatalf("cutoff = %d, want %d", got, want)
	}
}

func TestEventsPruneIntervalFallback(t *testing.T) {
	cases := []struct {
		in   string
		want time.Duration
	}{
		{"", time.Hour},
		{"30m", 30 * time.Minute},
		{"2h", 2 * time.Hour},
		{"nonsense", time.Hour},
		{"-5m", time.Hour},
	}
	for _, c := range cases {
		if got := (EventsConfig{PruneInterval: c.in}).EffectivePruneInterval(); got != c.want {
			t.Errorf("EffectivePruneInterval(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// An unbounded audit log is a disk-filling bug, so both limits being zero must
// be refused rather than silently accepted.
func TestEventsValidation(t *testing.T) {
	cases := []struct {
		name    string
		cfg     EventsConfig
		wantErr bool
	}{
		{"defaults", EventsConfig{RetentionDays: 7, MaxRows: 100_000, PruneInterval: "1h"}, false},
		{"age only", EventsConfig{RetentionDays: 3}, false},
		{"cap only", EventsConfig{MaxRows: 500}, false},
		{"both zero", EventsConfig{}, true},
		{"negative days", EventsConfig{RetentionDays: -1, MaxRows: 10}, true},
		{"negative rows", EventsConfig{RetentionDays: 7, MaxRows: -10}, true},
		{"interval too small", EventsConfig{RetentionDays: 7, PruneInterval: "10s"}, true},
		{"interval too large", EventsConfig{RetentionDays: 7, PruneInterval: "25h"}, true},
		{"interval unparsable", EventsConfig{RetentionDays: 7, PruneInterval: "hourly"}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := Defaults()
			cfg.Events = c.cfg
			err := cfg.Validate()
			if (err != nil) != c.wantErr {
				t.Fatalf("Validate() err = %v, wantErr = %v", err, c.wantErr)
			}
		})
	}
}

// The YAML key must actually parse, not just exist in Defaults.
func TestEventsYAMLOverlay(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := "events:\n  retentionDays: 3\n  maxRows: 5000\n  pruneInterval: \"15m\"\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Events.RetentionDays != 3 || cfg.Events.MaxRows != 5000 {
		t.Fatalf("events overlay failed: %+v", cfg.Events)
	}
	if d := cfg.Events.EffectivePruneInterval(); d != 15*time.Minute {
		t.Fatalf("prune interval = %v, want 15m", d)
	}
}
