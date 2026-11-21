package resolve

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/iawia002/lux/extractors"

	"github.com/asdfgh129/ud/internal/config"
	"github.com/asdfgh129/ud/internal/core"
	"github.com/asdfgh129/ud/internal/security"
)

// luxGuard allows loopback (offline fixtures) while blocking private ranges.
func luxGuard(t *testing.T) *security.SSRFGuard {
	t.Helper()
	return security.NewSSRFGuard(config.SecurityConfig{
		AllowedSchemes:       []string{"http", "https"},
		AllowedPorts:         []int{80, 443},
		MaxRedirects:         3,
		BlockPrivateNetworks: true,
		AllowLoopbackForDev:  true,
	})
}

// fixtureData builds one lux Data with a single-Part stream.
func fixtureData(title, streamID, url string, size int64) *extractors.Data {
	return &extractors.Data{
		URL:   "https://www.bilibili.com/video/x",
		Site:  "bilibili",
		Title: title,
		Type:  extractors.DataTypeVideo,
		Streams: map[string]*extractors.Stream{
			streamID: {Parts: []*extractors.Part{{URL: url, Size: size, Ext: "mp4"}}, Size: size},
		},
	}
}

func TestLuxMatchOnlyCuratedDomains(t *testing.T) {
	r := NewLuxResolver(nil, 0)
	yes := []string{
		"https://www.bilibili.com/video/BV123",
		"https://b23.tv/abc",
		"https://www.douyin.com/video/1",
		"https://www.iqiyi.com/v_1.html",
		"https://www.mgtv.com/b/1.html",
		"https://www.ixigua.com/1",
		"https://www.kuaishou.com/s/1",
	}
	for _, raw := range yes {
		if !r.Match(core.Input{Raw: raw, Kind: core.InputURL}) {
			t.Errorf("Match(%q) = false, want true", raw)
		}
	}
	no := []string{
		"https://www.youtube.com/watch?v=x",  // yt-dlp's territory
		"https://cdn.example.com/video.m3u8", // HLS transport
		"magnet:?xt=urn:btih:abc",
		"https://youku.com/v/1", // not in the curated extractor set
	}
	for _, raw := range no {
		if r.Match(core.Input{Raw: raw, Kind: core.InputURL}) {
			t.Errorf("Match(%q) = true, want false", raw)
		}
	}
	if r.Match(core.Input{Raw: "https://bilibili.com/x", Kind: core.InputMagnet}) {
		t.Error("non-URL kinds must not match")
	}
}

func TestLuxResolveBuildsResourceFromFixture(t *testing.T) {
	const mediaURL = "https://cdn.bilibili.com/seg0.mp4"
	r := newLuxResolverWithExtract(nil, time.Second, func(u string, _ extractors.Options) ([]*extractors.Data, error) {
		return []*extractors.Data{fixtureData("我的视频", "1080P 高码率", mediaURL, 12345)}, nil
	})
	res, err := r.Resolve(core.ResolveContext{Ctx: context.Background()},
		core.Input{Raw: "https://www.bilibili.com/video/BV123", Kind: core.InputURL})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if res.Kind != core.KindSingle {
		t.Fatalf("kind = %s", res.Kind)
	}
	if res.Files[0].Path != "我的视频.mp4" || res.Files[0].Size != 12345 {
		t.Fatalf("file = %+v", res.Files[0])
	}
	if res.Mirrors[0].URL != mediaURL {
		t.Fatalf("mirror = %s", res.Mirrors[0].URL)
	}
	// Site headers: Bilibili CDN requires the Referer (ADR-0026).
	if res.Headers["Referer"] != "https://www.bilibili.com/" {
		t.Fatalf("headers = %+v", res.Headers)
	}
	if res.Identity.VariantID != "lux:1080P 高码率" {
		t.Fatalf("variant = %q", res.Identity.VariantID)
	}
}

func TestLuxPickStreamLargestSizeDeterministic(t *testing.T) {
	data := &extractors.Data{
		Type: extractors.DataTypeVideo,
		Streams: map[string]*extractors.Stream{
			"720P":  {Parts: []*extractors.Part{{URL: "https://a/720", Size: 100}}, Size: 100},
			"1080P": {Parts: []*extractors.Part{{URL: "https://a/1080", Size: 500}}, Size: 500},
			"480P":  {Parts: []*extractors.Part{{URL: "https://a/480", Size: 500}}, Size: 500},
		},
	}
	got := pickStream(data.Streams)
	if got.ID != "1080P" {
		// Tie between 480P/1080P breaks to the lexicographically smaller ID.
		t.Fatalf("picked %q, want 1080P", got.ID)
	}
	if got.Parts[0].URL != "https://a/1080" {
		t.Fatalf("part = %s", got.Parts[0].URL)
	}
}

func TestLuxRejectsMultiPartStreams(t *testing.T) {
	r := newLuxResolverWithExtract(nil, time.Second, func(u string, _ extractors.Options) ([]*extractors.Data, error) {
		return []*extractors.Data{{
			Title: "long video", Type: extractors.DataTypeVideo,
			Streams: map[string]*extractors.Stream{
				"1080P": {Parts: []*extractors.Part{
					{URL: "https://cdn.bilibili.com/p0.mp4", Size: 1},
					{URL: "https://cdn.bilibili.com/p1.mp4", Size: 1},
				}, Size: 2},
			},
		}}, nil
	})
	_, err := r.Resolve(core.ResolveContext{Ctx: context.Background()},
		core.Input{Raw: "https://www.bilibili.com/video/BV1", Kind: core.InputURL})
	if err == nil || !strings.Contains(err.Error(), "multi-part") {
		t.Fatalf("err = %v, want the multi-part capability refusal", err)
	}
}

func TestLuxRejectsErrorsAndEmptyResults(t *testing.T) {
	// Extractor error is classified as transient network, not a crash.
	r := newLuxResolverWithExtract(nil, time.Second, func(u string, _ extractors.Options) ([]*extractors.Data, error) {
		return nil, errors.New("site page changed")
	})
	_, err := r.Resolve(core.ResolveContext{Ctx: context.Background()},
		core.Input{Raw: "https://www.bilibili.com/video/BV1", Kind: core.InputURL})
	if err == nil || !strings.Contains(err.Error(), "lux extraction failed") {
		t.Fatalf("err = %v, want a classified extraction failure", err)
	}

	// No video data → capability unavailable, not a fake success.
	r2 := newLuxResolverWithExtract(nil, time.Second, func(u string, _ extractors.Options) ([]*extractors.Data, error) {
		return []*extractors.Data{{Title: "x", Type: extractors.DataTypeImage, Err: errors.New("skip")}}, nil
	})
	_, err = r2.Resolve(core.ResolveContext{Ctx: context.Background()},
		core.Input{Raw: "https://www.bilibili.com/video/BV1", Kind: core.InputURL})
	if err == nil || !strings.Contains(err.Error(), "no usable video") {
		t.Fatalf("err = %v, want the no-video refusal", err)
	}
}

func TestLuxGuardBlocksPrivateMediaURL(t *testing.T) {
	r := newLuxResolverWithExtract(luxGuard(t), time.Second, func(u string, _ extractors.Options) ([]*extractors.Data, error) {
		return []*extractors.Data{fixtureData("x", "1080P", "http://10.99.99.99/seg.mp4", 10)}, nil
	})
	_, err := r.Resolve(core.ResolveContext{Ctx: context.Background()},
		core.Input{Raw: "https://www.bilibili.com/video/BV1", Kind: core.InputURL})
	if err == nil {
		t.Fatal("a private media URL produced by the extractor must be blocked at the exit")
	}
}

func TestLuxTimeoutAbandonsSlowExtractor(t *testing.T) {
	release := make(chan struct{})
	r := newLuxResolverWithExtract(nil, 30*time.Millisecond, func(u string, _ extractors.Options) ([]*extractors.Data, error) {
		<-release // simulates a stalled site; the goroutine leaks until done
		return nil, nil
	})
	start := time.Now()
	_, err := r.Resolve(core.ResolveContext{Ctx: context.Background()},
		core.Input{Raw: "https://www.bilibili.com/video/BV1", Kind: core.InputURL})
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("err = %v, want a timeout", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("Resolve blocked %s, want the 30ms bound", elapsed)
	}
	close(release) // let the leaked goroutine finish before the test exits
}

func TestLuxNonHTTPRefused(t *testing.T) {
	r := newLuxResolverWithExtract(nil, time.Second, func(u string, _ extractors.Options) ([]*extractors.Data, error) {
		t.Fatal("extract must not run for non-http schemes")
		return nil, nil
	})
	if _, err := r.Resolve(core.ResolveContext{}, core.Input{Raw: "ftp://bilibili.com/x", Kind: core.InputURL}); err == nil {
		t.Fatal("non-http(s) must be refused before extraction")
	}
}

func TestLuxConfigValidationAndTimeout(t *testing.T) {
	cfg := config.SourcesConfig{Lux: config.LuxConfig{Enabled: true, Timeout: "10s"}}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	if cfg.Lux.EffectiveTimeout() != 10*time.Second {
		t.Fatalf("timeout = %v", cfg.Lux.EffectiveTimeout())
	}
	bad := config.SourcesConfig{Lux: config.LuxConfig{Enabled: true, Timeout: "1h"}}
	if err := bad.Validate(); err == nil {
		t.Fatal("timeout beyond 5m must be rejected")
	}
	def := config.SourcesConfig{Lux: config.LuxConfig{Enabled: true}}
	if def.Lux.EffectiveTimeout() != 60*time.Second {
		t.Fatalf("default timeout = %v", def.Lux.EffectiveTimeout())
	}
}

func TestLuxForcedRoutingThroughRegistry(t *testing.T) {
	// Forced routing must REACH the lux resolver even when Match would say
	// no (registry-level override, ADR-0026) — but an out-of-list site is
	// then refused cleanly by lux's own domain pre-check instead of hitting
	// the library's nil-dispatch panic.
	g := NewRegistry()
	g.Register(NewLuxResolver(nil, time.Second))
	_, err := g.Resolve(core.ResolveContext{
		Ctx:     context.Background(),
		Options: map[string]any{OptLuxForce: true},
	}, core.Input{Raw: "https://unknown.example.com/video/1", Kind: core.InputURL})
	if err == nil || !strings.Contains(err.Error(), "no linked lux extractor") {
		t.Fatalf("err = %v, want the curated-set refusal (route must reach lux)", err)
	}

	// A supported domain with a seam-injected extractor: forced routing plus
	// extraction compose end to end.
	g2 := NewRegistry()
	g2.Register(newLuxResolverWithExtract(nil, time.Second,
		func(u string, _ extractors.Options) ([]*extractors.Data, error) {
			return []*extractors.Data{fixtureData("t", "720P", "https://cdn.bilibili.com/a.mp4", 1)}, nil
		}))
	res, err := g2.Resolve(core.ResolveContext{
		Ctx:     context.Background(),
		Options: map[string]any{OptLuxForce: true},
	}, core.Input{Raw: "https://m.bilibili.com/video/BV1", Kind: core.InputURL})
	if err != nil {
		t.Fatalf("forced+extract failed: %v", err)
	}
	if res.Identity.VariantID != "lux:720P" {
		t.Fatalf("variant = %q", res.Identity.VariantID)
	}
}

func TestLuxSubdomainCovered(t *testing.T) {
	// lux's own dispatch maps m.bilibili.com → the bilibili extractor; the
	// pre-check must accept subdomains while rejecting look-alikes.
	if !luxDomainCovered("m.bilibili.com") {
		t.Fatal("subdomain of a curated domain must be covered")
	}
	if luxDomainCovered("notbilibili.com") {
		t.Fatal("look-alike suffix must not pass")
	}
	if luxDomainCovered("") {
		t.Fatal("empty host must not pass")
	}
}
