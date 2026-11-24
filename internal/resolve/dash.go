package resolve

// The DASH resolver (WBS 13 second half, FR-TRN-08 / ADR-0024) classifies MPD
// URLs as stream resources before the generic direct-http fallback can
// download the manifest itself — the exact mirror of the HLS resolver.
// Classification is side-effect free: representation selection and segment
// fetching happen in the transport at Probe/Run.

import (
	"context"
	"net/url"
	"strings"

	"github.com/asdfgh129/gobitmule/internal/core"
)

// OptDASHForce forces DASH classification for tokenized URLs without the
// .mpd extension.
const OptDASHForce = "dash"

// DASHResolver marks MPD URLs as core.KindStream resources.
type DASHResolver struct {
	guard core.URLGuard
}

// NewDASHResolver builds the resolver. The guard is applied to the MPD URL at
// resolve time; BaseURL / init / segment URLs are checked by the transport.
func NewDASHResolver(g core.URLGuard) *DASHResolver { return &DASHResolver{guard: g} }

func (d *DASHResolver) Name() string { return "dash" }

func (d *DASHResolver) Match(in core.Input) bool {
	return in.Kind == core.InputURL && looksLikeDASHURL(in.Raw)
}

func looksLikeDASHURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	return strings.HasSuffix(strings.ToLower(u.Path), ".mpd")
}

func (d *DASHResolver) Resolve(rc core.ResolveContext, in core.Input) (*core.Resource, error) {
	// Only http(s) can be fetched by the DASH transport; the check is
	// unconditional so a misclassified ftp://x.mpd never becomes a task.
	if scheme := strings.ToLower(core.SchemeOf(in.Raw)); scheme != "http" && scheme != "https" {
		return nil, core.Err(core.E_INVALID_INPUT, "dash: only http(s) MPD URLs are supported")
	}
	if d.guard != nil {
		ctx := rc.Ctx
		if ctx == nil {
			ctx = context.Background()
		}
		if err := d.guard.Check(ctx, in.Raw); err != nil {
			return nil, err
		}
	}
	name := dashNameFor(in.Raw)
	return &core.Resource{
		ID:     "res-" + string(core.NewID()),
		Kind:   core.KindStream,
		Engine: core.EngineGo,
		Files: []core.ResourceFile{{
			Index: 0, Path: name, Selected: true,
		}},
		Mirrors: []core.Mirror{{URL: in.Raw}},
		Headers: map[string]string{
			core.HeaderStreamKind: core.StreamKindDASH,
		},
		Credential: rc.Credential,
		Identity: core.ProtocolIdentity{
			IsAdaptive: true,
		},
		Limits: core.TaskLimits{},
	}, nil
}

// dashNameFor derives the provisional filename; the transport replaces it at
// Probe with the MPD-derived name + .mp4.
func dashNameFor(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "stream.mp4"
	}
	p := strings.TrimSuffix(u.Path, "/")
	i := strings.LastIndexByte(p, '/')
	name := strings.TrimSuffix(p[i+1:], ".mpd")
	if name == "" {
		name = "stream"
	}
	return name + ".mp4"
}
