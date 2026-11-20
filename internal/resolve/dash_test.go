package resolve

import (
	"context"
	"testing"

	"github.com/asdfgh129/ud/internal/core"
)

func TestDASHMatchOnlyMPDURLs(t *testing.T) {
	r := NewDASHResolver(nil)
	yes := []string{
		"https://cdn.example.com/live/manifest.mpd",
		"https://cdn.example.com/live/manifest.mpd?token=abc",
		"http://a.example.COM/PLAYLIST.MPD",
	}
	for _, raw := range yes {
		if !r.Match(core.Input{Raw: raw, Kind: core.InputURL}) {
			t.Errorf("Match(%q) = false, want true", raw)
		}
	}
	no := []string{
		"https://cdn.example.com/live/master.m3u8",
		"https://cdn.example.com/video/watch/12345",
		"magnet:?xt=urn:btih:abc",
	}
	for _, raw := range no {
		if r.Match(core.Input{Raw: raw, Kind: core.InputURL}) {
			t.Errorf("Match(%q) = true, want false", raw)
		}
	}
}

func TestDASHResolveProducesStreamResourceWithMarker(t *testing.T) {
	r := NewDASHResolver(nil)
	res, err := r.Resolve(core.ResolveContext{}, core.Input{
		Raw: "https://cdn.example.com/live/manifest.mpd?tok=1", Kind: core.InputURL,
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if res.Kind != core.KindStream {
		t.Fatalf("kind = %s, want stream", res.Kind)
	}
	if res.Headers[core.HeaderStreamKind] != core.StreamKindDASH {
		t.Fatalf("routing marker missing: %+v", res.Headers)
	}
	if !res.Identity.IsAdaptive {
		t.Fatal("stream resources must be marked adaptive")
	}
	if res.Files[0].Path != "manifest.mp4" {
		t.Fatalf("provisional filename = %q", res.Files[0].Path)
	}
}

func TestDASHResolveRefusesNonHTTP(t *testing.T) {
	r := NewDASHResolver(nil)
	if _, err := r.Resolve(core.ResolveContext{}, core.Input{
		Raw: "ftp://cdn.example.com/live.mpd", Kind: core.InputURL,
	}); err == nil {
		t.Fatal("non-http(s) MPD must be refused")
	}
}

func TestDASHForceClassification(t *testing.T) {
	r := NewDASHResolver(nil)
	raw := "https://cdn.example.com/manifest/abc123"
	if r.Match(core.Input{Raw: raw, Kind: core.InputURL}) {
		t.Fatal("tokenized URL must not match by default")
	}
	// Forced classification flows through POST /tasks options: the scheduler
	// hands them to the resolve phase via ResolveContext.Options.
	res, err := r.Resolve(core.ResolveContext{
		Ctx:     context.Background(),
		Options: map[string]any{OptDASHForce: true},
	}, core.Input{Raw: raw, Kind: core.InputURL})
	if err != nil {
		t.Fatalf("forced Resolve: %v", err)
	}
	if res.Kind != core.KindStream || res.Headers[core.HeaderStreamKind] != core.StreamKindDASH {
		t.Fatalf("forced resource = kind %s headers %+v", res.Kind, res.Headers)
	}
}
