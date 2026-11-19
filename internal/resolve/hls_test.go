package resolve

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/asdfgh129/ud/internal/config"
	"github.com/asdfgh129/ud/internal/core"
	"github.com/asdfgh129/ud/internal/security"
)

func TestHLSMatchOnlyM3U8URLs(t *testing.T) {
	r := NewHLSResolver(nil)
	yes := []string{
		"https://cdn.example.com/live/master.m3u8",
		"https://cdn.example.com/live/master.m3u8?token=abc#frag",
		"http://a.example.COM/PLAYLIST.M3U8",
	}
	for _, raw := range yes {
		if !r.Match(core.Input{Raw: raw, Kind: core.InputURL}) {
			t.Errorf("Match(%q) = false, want true", raw)
		}
	}
	no := []string{
		"https://cdn.example.com/video/watch/12345", // page URL: WBS 12's job
		"https://cdn.example.com/seg.m4s",
		"magnet:?xt=urn:btih:abc",
	}
	for _, raw := range no {
		if r.Match(core.Input{Raw: raw, Kind: core.InputURL}) {
			t.Errorf("Match(%q) = true, want false", raw)
		}
	}
	if r.Match(core.Input{Raw: "https://x.m3u8", Kind: core.InputMagnet}) {
		t.Error("non-URL kinds must not match")
	}
}

func TestHLSResolveProducesStreamResource(t *testing.T) {
	r := NewHLSResolver(nil)
	res, err := r.Resolve(core.ResolveContext{}, core.Input{
		Raw: "https://cdn.example.com/live/master.m3u8?tok=1", Kind: core.InputURL,
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if res.Kind != core.KindStream {
		t.Fatalf("kind = %s, want stream", res.Kind)
	}
	if len(res.Mirrors) != 1 || res.Mirrors[0].URL != "https://cdn.example.com/live/master.m3u8?tok=1" {
		t.Fatalf("mirrors = %+v", res.Mirrors)
	}
	if !res.Identity.IsAdaptive {
		t.Fatal("stream resources must be marked adaptive")
	}
	if res.Files[0].Path == "" {
		t.Fatal("provisional filename missing")
	}
}

func TestHLSResolveChecksGuard(t *testing.T) {
	// A loopback listener is allowed for dev, but 10/8 must be refused.
	guard := security.NewSSRFGuard(config.SecurityConfig{
		AllowedSchemes:       []string{"http", "https"},
		AllowedPorts:         []int{80, 443},
		MaxRedirects:         3,
		BlockPrivateNetworks: true,
		AllowLoopbackForDev:  true,
	})
	r := NewHLSResolver(guard)
	if _, err := r.Resolve(core.ResolveContext{Ctx: context.Background()},
		core.Input{Raw: "http://10.99.99.99/master.m3u8", Kind: core.InputURL}); err == nil {
		t.Fatal("private playlist host must be blocked at resolve time")
	}
	// Loopback (the httptest fixture) must pass.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer srv.Close()
	if _, err := r.Resolve(core.ResolveContext{Ctx: context.Background()},
		core.Input{Raw: srv.URL + "/master.m3u8", Kind: core.InputURL}); err != nil {
		t.Fatalf("loopback playlist refused: %v", err)
	}
}

func TestHLSForcedClassification(t *testing.T) {
	r := NewHLSResolver(nil)
	raw := "https://cdn.example.com/manifest/abc123"
	// Without the option the URL is not an HLS match at all.
	if r.Match(core.Input{Raw: raw, Kind: core.InputURL}) {
		t.Fatal("tokenized URL must not match by default")
	}
	// With {"hls": true} the resolve step still produces a stream resource.
	res, err := r.Resolve(core.ResolveContext{
		Ctx:     context.Background(),
		Options: map[string]any{OptHLSForce: true},
	}, core.Input{Raw: raw, Kind: core.InputURL})
	if err != nil {
		t.Fatalf("forced Resolve: %v", err)
	}
	if res.Kind != core.KindStream {
		t.Fatalf("forced kind = %s", res.Kind)
	}
	// Forced mode still refuses non-http schemes.
	if _, err := r.Resolve(core.ResolveContext{
		Ctx:     context.Background(),
		Options: map[string]any{OptHLSForce: true},
	}, core.Input{Raw: "ftp://cdn.example.com/x.m3u8", Kind: core.InputURL}); err == nil {
		t.Fatal("forced classification must refuse non-http schemes")
	}
}
