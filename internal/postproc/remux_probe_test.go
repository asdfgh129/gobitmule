package postproc

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/asdfgh129/ud/internal/core"
)

// TestRemuxRealTSFixture reads an externally generated MPEG-TS file from
// UD_REMUX_TS (a real ffmpeg-built fixture, never part of the repo). It is a
// manual verification seam, skipped by default; CI uses the synthetic fixtures
// in remux_test.go.
func TestRemuxRealTSFixture(t *testing.T) {
	src := os.Getenv("UD_REMUX_TS")
	if src == "" {
		t.Skip("UD_REMUX_TS not set")
	}
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	out := filepath.Join(dir, "out.mp4")
	res, err := (Remux{}).Process(context.Background(),
		core.LocalFiles{BaseDir: dir, Paths: []string{src}},
		core.OutputSpec{Target: out, TempSuffix: ".part"})
	if err != nil {
		t.Fatalf("remux: %v (%d bytes TS)", err, len(data))
	}
	got, err := os.ReadFile(res.Outputs[0])
	if err != nil {
		t.Fatal(err)
	}
	dst := os.Getenv("UD_REMUX_OUT")
	if dst != "" {
		if err := os.WriteFile(dst, got, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("TS %d bytes -> MP4 %d bytes", len(data), len(got))
}
