package resolve

import (
	"context"
	"testing"

	"github.com/asdfgh129/ud/internal/core"
)

func TestLuxResolverMatch(t *testing.T) {
	yes := []string{
		"https://www.bilibili.com/video/BV123456",
		"https://v.qq.com/x/cover/abc",
		"https://www.iqiyi.com/v_123.html",
	}
	no := []string{
		"magnet:?xt=urn:btih:abc",
		"https://github.com/user/repo",
		"ftp://example.com/file",
		// YouTube is owned by the yt-dlp resolver, not the in-process lux rule
		// channel (docs/05 §10: one owner per input).
		"https://www.youtube.com/watch?v=abc",
	}
	for _, raw := range yes {
		if !NewLuxResolver(nil, nil).Match(core.Input{Raw: raw, Kind: core.InputURL}) {
			t.Errorf("Match(%q) = false, want true", raw)
		}
	}
	for _, raw := range no {
		if NewLuxResolver(nil, nil).Match(core.Input{Raw: raw, Kind: core.InputURL}) {
			t.Errorf("Match(%q) = true, want false", raw)
		}
	}
	// Non-URL kinds must not match
	if NewLuxResolver(nil, nil).Match(core.Input{Raw: "https://bilibili.com/video", Kind: core.InputMagnet}) {
		t.Error("magnet kind must not match")
	}
}

func TestLuxResolverResolve(t *testing.T) {
	r := NewLuxResolver(nil, nil)
	res, err := r.Resolve(core.ResolveContext{}, core.Input{
		Raw: "https://www.bilibili.com/video/BV123456", Kind: core.InputURL,
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if res.Kind != core.KindSingle {
		t.Fatalf("kind = %s, want single", res.Kind)
	}
	if res.Identity.VariantID != "lux" {
		t.Fatalf("variant = %s, want lux", res.Identity.VariantID)
	}
	if res.Mirrors[0].URL != "https://www.bilibili.com/video/BV123456" {
		t.Fatalf("mirror = %s", res.Mirrors[0].URL)
	}
}

func TestLuxResolverForcedClassification(t *testing.T) {
	r := NewLuxResolver(nil, nil)
	// Without forced option, unknown domain should not match
	if r.Match(core.Input{Raw: "https://unknown.example.com/video", Kind: core.InputURL}) {
		t.Fatal("unknown domain should not match by default")
	}
	// With the forced option, Resolve accepts http(s) URLs outside the
	// built-in domain list (ADR-0023/0024 forced-classification pattern).
	res, err := r.Resolve(core.ResolveContext{
		Ctx:     context.Background(),
		Options: map[string]any{OptLuxForce: true},
	}, core.Input{Raw: "https://unknown.example.com/video", Kind: core.InputURL})
	if err != nil {
		t.Fatalf("forced Resolve: %v", err)
	}
	if res.Kind != core.KindSingle || res.Identity.VariantID != "lux" {
		t.Fatalf("forced resolve: kind=%s variant=%s", res.Kind, res.Identity.VariantID)
	}
	// Forced non-http must be rejected
	_, err = r.Resolve(core.ResolveContext{
		Options: map[string]any{OptLuxForce: true},
	}, core.Input{Raw: "ftp://example.com/file", Kind: core.InputURL})
	if err == nil {
		t.Fatal("forced lux must reject non-http scheme")
	}
}

func TestYtDlpResolverMatch(t *testing.T) {
	yes := []string{
		"https://www.youtube.com/watch?v=abc",
		"https://youtu.be/xyz",
		"https://vimeo.com/12345",
		"https://www.twitch.tv/user",
		"https://twitter.com/user/status/123",
	}
	no := []string{
		"magnet:?xt=urn:btih:abc",
		"https://www.bilibili.com/video/BV123",
	}
	for _, raw := range yes {
		if !NewYtDlpResolver(nil, nil).Match(core.Input{Raw: raw, Kind: core.InputURL}) {
			t.Errorf("Match(%q) = false, want true", raw)
		}
	}
	for _, raw := range no {
		if NewYtDlpResolver(nil, nil).Match(core.Input{Raw: raw, Kind: core.InputURL}) {
			t.Errorf("Match(%q) = true, want false", raw)
		}
	}
}

func TestYtDlpResolverResolve(t *testing.T) {
	r := NewYtDlpResolver(nil, nil)
	res, err := r.Resolve(core.ResolveContext{}, core.Input{
		Raw: "https://www.youtube.com/watch?v=abc", Kind: core.InputURL,
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if res.Kind != core.KindSingle {
		t.Fatalf("kind = %s", res.Kind)
	}
	if res.Identity.VariantID != "yt-dlp" {
		t.Fatalf("variant = %s", res.Identity.VariantID)
	}
}

func TestYtDlpForcedClassification(t *testing.T) {
	r := NewYtDlpResolver(nil, nil)
	// Without forced option, unknown domain should not match
	if r.Match(core.Input{Raw: "https://unknown.example.com/video", Kind: core.InputURL}) {
		t.Fatal("unknown domain should not match by default")
	}
	// With the forced option, Resolve accepts http(s) URLs outside the
	// built-in domain list.
	res, err := NewYtDlpResolver(nil, nil).Resolve(core.ResolveContext{
		Ctx:     context.Background(),
		Options: map[string]any{OptYtDlpForce: true},
	}, core.Input{Raw: "https://unknown.example.com/video", Kind: core.InputURL})
	if err != nil {
		t.Fatalf("forced Resolve: %v", err)
	}
	if res.Identity.VariantID != "yt-dlp" {
		t.Fatalf("variant = %s", res.Identity.VariantID)
	}
	// Forced non-http must be rejected
	_, err = NewYtDlpResolver(nil, nil).Resolve(core.ResolveContext{
		Options: map[string]any{OptYtDlpForce: true},
	}, core.Input{Raw: "ftp://example.com/file", Kind: core.InputURL})
	if err == nil {
		t.Fatal("forced yt-dlp must reject non-http scheme")
	}
}

func TestSelectBestFormat(t *testing.T) {
	formats := []YtDlpFormat{
		{FormatID: "137", Ext: "mp4", Height: 1080, VCodec: "avc1", ACodec: "mp4a.40.2", TBR: 5000},
		{FormatID: "248", Ext: "webm", Height: 1080, VCodec: "vp9", ACodec: "opus", TBR: 4500},
		{FormatID: "399", Ext: "mp4", Height: 2160, VCodec: "av01", ACodec: "opus", TBR: 15000},
		{FormatID: "18", Ext: "mp4", Height: 360, VCodec: "avc1", ACodec: "mp4a.40.2", TBR: 800},
	}
	policy := DefaultFormatSelectPolicy()
	best := SelectBestFormat(formats, policy)
	if best == nil || best.FormatID != "399" {
		t.Fatalf("best = %v, want format 399 (AV1 4K)", best)
	}
	// Test with max height constraint
	policy.MaxHeight = 1080
	best = SelectBestFormat(formats, policy)
	if best == nil || best.FormatID != "137" {
		t.Fatalf("best with max 1080 = %v, want 137", best)
	}
	// Empty formats
	if SelectBestFormat(nil, policy) != nil {
		t.Fatal("empty formats must return nil")
	}
	// Formats all filtered out
	policy.MaxHeight = 100
	if SelectBestFormat(formats, policy) != nil {
		t.Fatal("all filtered must return nil")
	}
}
