// Package wechat implements the isolated, opt-in WeChat content plugin
// (WBS 18 / ADR-0032). It is deliberately a separate package and a separate
// distribution unit: the main daemon never registers it unless the operator
// explicitly opts in, and /capabilities reports "unavailable" by default.
//
// Scope implemented here (offline, locally verifiable):
//   - public 公众号 (Official Account) article HTML → core.Resource, using
//     the same URL/Header model as any other site resolver;
//   - the capability descriptor and privilege-helper IPC contract (interfaces
//     only), so an independent helper binary can be built separately.
//
// NOT implemented here (real-environment, NOT_EXECUTED per ADR-0032):
//   - 视频号 MITM interception, JS injection, private cipher reversal;
//   - root CA install / system proxy mutation performed by the helper.
//
// Compliance red lines (ADR-0018 / ADR-0032) enforced in code: no DRM, no
// member-content bypass, no account sharing, desktop-only, explicit consent.
package wechat

import (
	"context"
	"strings"

	"github.com/asdfgh129/gobitmule/internal/core"
)

const (
	// CapabilityUnavailable is what the main build reports on /capabilities
	// until an operator installs the separate WeChat plugin unit.
	CapabilityUnavailable = "unavailable"
	// CapabilityAvailable is reported only when the opt-in resolver is wired.
	CapabilityAvailable = "available"
	// PluginName is stable across the RPC/capability surface.
	PluginName = "wechat"
)

// Host allowlist: only public mp.weixin.qq.com article pages are parsed.
// The 视频号 surface has no public API and is intentionally absent here.
var publicArticleHosts = []string{"mp.weixin.qq.com"}

// IsPublicArticle reports whether raw is a public Official Account article URL
// that this offline resolver can handle. It never matches 视频号 or app-only
// deep links, which require the isolated MITM plugin unit.
func IsPublicArticle(raw string) bool {
	lower := strings.ToLower(strings.TrimSpace(raw))
	if !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://") {
		return false
	}
	rest := lower[strings.Index(lower, "://")+3:]
	host := rest
	if i := strings.IndexAny(rest, "/?#"); i >= 0 {
		host = rest[:i]
	}
	if i := strings.LastIndex(host, "@"); i >= 0 {
		host = host[i+1:]
	}
	if i := strings.LastIndex(host, ":"); i >= 0 && !strings.Contains(host[i:], "]") {
		host = host[:i]
	}
	for _, h := range publicArticleHosts {
		if host == h || strings.HasSuffix(host, "."+h) {
			return true
		}
	}
	return false
}

// ArticleResolver parses a public 公众号 article URL into a Resource. It is
// opt-in: the main daemon must explicitly register it, which only happens when
// the separate plugin unit is installed and enabled. No network I/O occurs in
// Match; resolution of the actual media URL is delegated to the caller's
// extractor (injected, so CI never needs WeChat access).
type ArticleResolver struct {
	guard  core.URLGuard
	detect ArticleDetector
}

// ArticleDetector extracts the media source and title from an article page.
// Production supplies an HTML-based implementation; tests inject a fake so
// no real WeChat request is ever made (docs/12 §1).
type ArticleDetector interface {
	Detect(ctx context.Context, raw string) (mediaURL, title string, err error)
}

func NewArticleResolver(g core.URLGuard, d ArticleDetector) *ArticleResolver {
	return &ArticleResolver{guard: g, detect: d}
}

func (r *ArticleResolver) Name() string { return "wechat-article" }

func (r *ArticleResolver) Match(in core.Input) bool {
	return IsPublicArticle(in.Raw)
}

// Resolve is intentionally minimal: it validates the input class and records
// the source URL on the Resource. The injected detector performs the actual
// HTML extraction; a nil detector yields an explicit capability error rather
// than a panic (same double-defence pattern as ADR-0026).
func (r *ArticleResolver) Resolve(ctx core.ResolveContext, in core.Input) (*core.Resource, error) {
	if !r.Match(in) {
		return nil, core.Err(core.E_INVALID_INPUT, "input is not a public WeChat article URL")
	}
	if r.detect == nil {
		return nil, core.Err(core.E_CAPABILITY_UNAVAILABLE,
			"WeChat article detector not installed; install the isolated plugin unit")
	}
	if err := r.guard.Check(ctx.Ctx, in.Raw); err != nil {
		return nil, err
	}
	mediaURL, title, err := r.detect.Detect(ctx.Ctx, in.Raw)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(title)
	if name == "" {
		name = "wechat-article"
	}
	return &core.Resource{
		ID:     "res-" + string(core.NewID()),
		Kind:   core.KindSingle,
		Engine: core.EngineGo,
		Files:  []core.ResourceFile{{Index: 0, Path: sanitizeName(name), Selected: true}},
		Mirrors: []core.Mirror{{URL: mediaURL}},
		Headers: map[string]string{},
		Credential: ctx.Credential,
		Limits: core.TaskLimits{},
	}, nil
}

func sanitizeName(s string) string {
	replacer := strings.NewReplacer("/", "_", "\\", "_", ":", "_", "*", "_", "?", "_", "\"", "_", "<", "_", ">", "_", "|", "_")
	name := strings.TrimSpace(replacer.Replace(s))
	if name == "" {
		return "wechat-article"
	}
	return name
}
