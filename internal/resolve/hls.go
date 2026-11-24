package resolve

// The HLS resolver (WBS 13 / FR-TRN-08) classifies playlist URLs as stream
// resources BEFORE the generic direct-http fallback can download the playlist
// file itself. Classification is deliberately cheap and side-effect free:
// the transport owns every network round trip (variant selection, segment
// fetch, decryption) so the resolve phase never duplicates them.
//
// Match rules (ADR-0023 L1):
//   - URL path ends in ".m3u8" (query string allowed) — the standard case;
//   - an explicit `{"hls": true}` task option forces classification for
//     tokenized CDN URLs that hide the extension.
//
// Everything else — content sniffing, YouTube page handling — is explicitly
// out of L1 scope; yt-dlp-based site resolution is WBS 12.

import (
	"context"
	"net/url"
	"strings"

	"github.com/asdfgh129/gobitmule/internal/core"
)

// OptHLSForce forces HLS classification regardless of the URL extension.
const OptHLSForce = "hls"

// HLSResolver marks playlist URLs as core.KindStream resources.
type HLSResolver struct {
	guard core.URLGuard
}

// NewHLSResolver builds the resolver. The guard is applied to the playlist
// URL at resolve time; segment / variant / key URLs are checked by the
// transport when it actually fetches them.
func NewHLSResolver(g core.URLGuard) *HLSResolver { return &HLSResolver{guard: g} }

func (h *HLSResolver) Name() string { return "hls" }

func (h *HLSResolver) Match(in core.Input) bool {
	return in.Kind == core.InputURL && looksLikeHLSURL(in.Raw)
}

// looksLikeHLSURL reports whether the URL path ends in .m3u8, ignoring any
// query or fragment. Scheme-agnostic on purpose: the SSRF Guard rejects
// non-http schemes later.
func looksLikeHLSURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	path := strings.ToLower(u.Path)
	return strings.HasSuffix(path, ".m3u8")
}

func (h *HLSResolver) Resolve(rc core.ResolveContext, in core.Input) (*core.Resource, error) {
	// Only http(s) can be fetched by the HLS transport; everything else is a
	// job for the dedicated transports. The check is unconditional so a
	// misclassified ftp://x.m3u8 never becomes a stream task.
	if scheme := strings.ToLower(core.SchemeOf(in.Raw)); scheme != "http" && scheme != "https" {
		return nil, core.Err(core.E_INVALID_INPUT, "hls: only http(s) playlist URLs are supported")
	}
	if _, ok := rc.Options[OptHLSForce].(bool); ok {
		// Forced mode accepts extension-less tokenized URLs; nothing else to
		// validate here — the SSRF guard below covers the address.
	}
	if h.guard != nil {
		ctx := rc.Ctx
		if ctx == nil {
			ctx = context.Background()
		}
		if err := h.guard.Check(ctx, in.Raw); err != nil {
			return nil, err
		}
	}
	name := streamNameFor(in.Raw)
	return &core.Resource{
		ID:     "res-" + string(core.NewID()),
		Kind:   core.KindStream,
		Engine: core.EngineGo,
		Files: []core.ResourceFile{{
			Index: 0, Path: name, Selected: true,
		}},
		Mirrors: []core.Mirror{{URL: in.Raw}},
		Headers: map[string]string{
			// Internal routing marker (never reaches the wire; the transport
			// strips it) so dispatch survives restarts and extension-less
			// forced URLs pick the right byte mover (ADR-0024).
			core.HeaderStreamKind: core.StreamKindHLS,
		},
		Credential: rc.Credential,
		Identity: core.ProtocolIdentity{
			IsAdaptive: true, // confirmed or corrected by the transport at Probe
		},
		Limits: core.TaskLimits{},
	}, nil
}

// streamNameFor derives a provisional output filename; the transport replaces
// it with the playlist-derived name at Probe (including the .ts / .mp4
// container decision).
func streamNameFor(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "stream.ts"
	}
	p := strings.TrimSuffix(u.Path, "/")
	i := strings.LastIndexByte(p, '/')
	name := strings.TrimSuffix(p[i+1:], ".m3u8")
	if name == "" {
		name = "stream"
	}
	return name + ".ts"
}
