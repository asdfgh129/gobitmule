package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The P99 used for the create->first-byte criterion must be the documented
// nearest-rank percentile; an off-by-one here would misreport the verdict.
func TestPercentileNearestRank(t *testing.T) {
	sorted := []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	cases := []struct {
		p    float64
		want float64
	}{
		{0, 1},
		{50, 6},
		{99, 10},
		{100, 10},
	}
	for _, c := range cases {
		if got := percentile(sorted, c.p); got != c.want {
			t.Errorf("percentile(%v) = %v, want %v", c.p, got, c.want)
		}
	}
	if got := percentile(nil, 99); got != 0 {
		t.Errorf("percentile(empty) = %v, want 0", got)
	}
}

func TestSeriesStatsReportsPeakNotJustLast(t *testing.T) {
	max, min, last := seriesStats([]float64{10, 42, 20, 15})
	if max != 42 || min != 10 || last != 15 {
		t.Fatalf("seriesStats = (%v,%v,%v), want (42,10,15)", max, min, last)
	}
	if m, mi, l := seriesStats(nil); m != 0 || mi != 0 || l != 0 {
		t.Fatalf("seriesStats(nil) = (%v,%v,%v), want zeros", m, mi, l)
	}
}

func TestShortDur(t *testing.T) {
	cases := map[time.Duration]string{
		2 * time.Hour:    "2h",
		90 * time.Minute: "1h30m0s",
		30 * time.Second: "30s",
	}
	for in, want := range cases {
		if got := shortDur(in); got != want {
			t.Errorf("shortDur(%v) = %q, want %q", in, got, want)
		}
	}
}

// The generated config is what lets the real SSRF guard accept a loopback
// fixture on an ephemeral port; dropping either key silently breaks every
// phase with E_RESOLVE_SSRF_BLOCKED.
func TestWriteBenchConfigKeepsLoopbackFixtureAllowed(t *testing.T) {
	dir := t.TempDir()
	path, err := writeBenchConfig(dir, 45678)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg := string(raw)
	for _, needle := range []string{
		"listen: 127.0.0.1:45678",
		"allowLoopbackForDev: true",
		"blockPrivateNetworks: false",
		"maxConcurrentTasks: 5",
	} {
		if !strings.Contains(cfg, needle) {
			t.Fatalf("config is missing %q:\n%s", needle, cfg)
		}
	}
	if !strings.Contains(cfg, filepath.Join(dir, "dl")) {
		t.Fatalf("config must point downloadDir into the bench dir:\n%s", cfg)
	}
}

// A shorter soak window is measured but can never satisfy the 2h criterion, so
// the tool must refuse to mark it met.
func TestSoakWindowBelowSpecIsNotMet(t *testing.T) {
	if soakSpecified != 2*time.Hour {
		t.Fatalf("soakSpecified = %v, want 2h (docs/12 §5)", soakSpecified)
	}
	if time.Hour >= soakSpecified {
		t.Fatal("a 1h window must be below the specified soak")
	}
}

// The startup criterion must use the healthz clock (which already contains the
// restore) and must also require every task to be back. A fast listen with a
// short task list is not a pass.
func TestStartupMetRequiresInclusiveClockAndAllTasks(t *testing.T) {
	cases := []struct {
		name     string
		readyMs  int64
		restored int64
		tasks    int64
		want     bool
	}{
		{"inside budget with all tasks", 850, 1000, 1000, true},
		{"exactly at budget", 3000, 1000, 1000, true},
		{"one ms over budget", 3001, 1000, 1000, false},
		{"missing tasks", 850, 999, 1000, false},
	}
	for _, c := range cases {
		if got := startupMet(c.readyMs, c.restored, c.tasks); got != c.want {
			t.Errorf("%s: startupMet(%d,%d,%d) = %t, want %t",
				c.name, c.readyMs, c.restored, c.tasks, got, c.want)
		}
	}
}
