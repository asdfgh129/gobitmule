package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPostProcessDefaultsAreOptIn(t *testing.T) {
	c := Defaults()
	if c.PostProcess.FFmpegEnabled {
		t.Fatal("ffmpeg must be opt-in to preserve the zero-sidecar baseline")
	}
	if c.PostProcess.FFmpegBinary != "ffmpeg" || c.PostProcess.FFmpegThreads != 2 ||
		c.PostProcess.FFmpegMaxConcurrent != 1 || c.PostProcess.EffectiveTimeout() != 30*time.Minute {
		t.Fatalf("unexpected postProcess defaults: %+v", c.PostProcess)
	}
}

func TestPostProcessValidateFailClosed(t *testing.T) {
	base := Defaults().PostProcess
	base.FFmpegEnabled = true
	if err := base.Validate(); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		mutate func(*PostProcessConfig)
	}{
		{"empty binary", func(p *PostProcessConfig) { p.FFmpegBinary = "" }},
		{"control chars", func(p *PostProcessConfig) { p.FFmpegBinary = "ffmpeg\nx" }},
		{"threads zero", func(p *PostProcessConfig) { p.FFmpegThreads = 0 }},
		{"concurrency zero", func(p *PostProcessConfig) { p.FFmpegMaxConcurrent = 0 }},
		{"timeout too short", func(p *PostProcessConfig) { p.FFmpegTimeout = "5s" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := base
			tc.mutate(&p)
			if err := p.Validate(); err == nil {
				t.Fatal("invalid postProcess config must fail closed")
			}
		})
	}
}

func TestLoadPostProcessYAMLAndEnv(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	yaml := `postProcess:
  ffmpegEnabled: true
  ffmpegBinary: /opt/ffmpeg
  ffmpegThreads: 3
  ffmpegMaxConcurrent: 2
  ffmpegTimeout: 12m
`
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("UD_FFMPEG_THREADS", "4")
	t.Setenv("UD_FFMPEG_ENABLED", "true")
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	p := c.PostProcess
	if !p.FFmpegEnabled || p.FFmpegBinary != "/opt/ffmpeg" || p.FFmpegThreads != 4 ||
		p.FFmpegMaxConcurrent != 2 || p.EffectiveTimeout() != 12*time.Minute {
		t.Fatalf("postProcess config not applied: %+v", p)
	}
}

func TestLoadRejectsBadPostProcessConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("postProcess:\n  ffmpegEnabled: true\n  ffmpegTimeout: 3s\n  ffmpegBinary: /opt/ffmpeg\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "postProcess") {
		t.Fatalf("want postProcess validation error, got %v", err)
	}
}
