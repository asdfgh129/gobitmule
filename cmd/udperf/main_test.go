package main

import (
	"context"
	"testing"
)

func TestNFRVerdictFor(t *testing.T) {
	cases := []struct {
		name     string
		twoHost  bool
		mbps     float64
		minMBs   float64
		verified bool
		wantMet  bool
	}{
		{"loopback never meets NFR", false, 9999, 10, true, false},
		{"two-host without threshold", true, 9999, 0, true, false},
		{"two-host below threshold", true, 9.99, 10, true, false},
		{"two-host at threshold", true, 10, 10, true, true},
		{"two-host above threshold", true, 11.21, 10, true, true},
		{"two-host unverified bytes", true, 20, 10, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := nfrVerdictFor(tc.twoHost, tc.mbps, tc.minMBs, tc.verified)
			if got.Met != tc.wantMet {
				t.Fatalf("met = %v, want %v (reason: %s)", got.Met, tc.wantMet, got.Reason)
			}
		})
	}
}

func TestClientGuardTwoHostAllowsPrivateSegmentOnly(t *testing.T) {
	ctx := context.Background()
	labURL := "http://192.168.1.221:18080/perf.bin"
	g, err := clientGuard(labURL, true)
	if err != nil {
		t.Fatalf("clientGuard: %v", err)
	}
	if err := g.Check(ctx, labURL); err != nil {
		t.Fatalf("lab URL should be allowed: %v", err)
	}
	if err := g.Check(ctx, "http://192.168.1.221:9999/perf.bin"); err == nil {
		t.Fatal("non-pinned port must be rejected")
	}

	loop, err := clientGuard("http://127.0.0.1:18080/perf.bin", false)
	if err != nil {
		t.Fatalf("loopback clientGuard: %v", err)
	}
	if err := loop.Check(ctx, labURL); err == nil {
		t.Fatal("loopback dev guard must keep private addresses blocked")
	}
}

func TestPatternFileDeterministicAndSeekable(t *testing.T) {
	pattern := makePattern()
	if len(pattern) != patternSize {
		t.Fatalf("pattern size = %d, want %d", len(pattern), patternSize)
	}
	p := newPatternFile(2*patternSize, pattern)
	first := make([]byte, patternSize)
	if n, err := p.Read(first); err != nil || n != len(first) {
		t.Fatalf("read = %d, %v", n, err)
	}
	if _, err := p.Seek(0, 0); err != nil {
		t.Fatalf("seek: %v", err)
	}
	second := make([]byte, patternSize)
	if n, err := p.Read(second); err != nil || n != len(second) {
		t.Fatalf("second read = %d, %v", n, err)
	}
	for i := range first {
		if first[i] != second[i] {
			t.Fatalf("pattern differs at byte %d", i)
		}
	}
}
