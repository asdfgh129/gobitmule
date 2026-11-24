package resolve

// LuxResolver implements the in-process half of ADR-0010 decision 1 (WBS 12,
// FR-SRC-01): real extraction for SANCTIONED Chinese video sites through the
// lux library, with every produced URL re-validated by the SSRF Guard at the
// resolver exit (docs/05 §10).
//
// Dependency discipline (measured 2026-11-20): the curated extractor subset —
// bilibili / douyin / iqiyi / mgtv / ixigua / kuaishou — links 18 modules, all
// MIT/BSD; the lux CLI stack (urfave/cli, progress bar), the colly scraper
// framework and the second JS engine (otto) stay OUT of the build because
// site extractors self-register via init() and only imported ones link.
// Importing the full `extractors` tree would link 160+ modules and is
// deliberately avoided.
//
// L1 boundaries (honest):
//   - single-Part streams only. The scheduler downloads Resource.Files[0];
//     multi-Part (concatenated) streams are refused with
//     E_CAPABILITY_UNAVAILABLE rather than silently dropping parts.
//   - lux's Extract API is not context-aware: the timeout ABANDONS the call
//     (the leaked goroutine finishes in the background, bounded by the
//     extraction itself) — documented, not hidden.
//   - extraction makes same-site network requests (page HTML / APIs) inside
//     lux; the page URL itself was guard-checked before the call, and every
//     produced media URL is guard-checked here at the exit.

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/iawia002/lux/extractors"

	"github.com/asdfgh129/gobitmule/internal/core"

	// Curated extractor subset (ADR-0026): only sanctioned Chinese video
	// sites link in. Site extractors self-register via init(), so the
	// unlisted ones (and the lux CLI / scraper / second-JS-engine footprint)
	// never join the build.
	_ "github.com/iawia002/lux/extractors/bilibili"
	_ "github.com/iawia002/lux/extractors/douyin"
	_ "github.com/iawia002/lux/extractors/iqiyi"
	_ "github.com/iawia002/lux/extractors/ixigua"
	_ "github.com/iawia002/lux/extractors/kuaishou"
	_ "github.com/iawia002/lux/extractors/mgtv"
)

const (
	// OptLuxForce forces lux classification for extension-less or off-list
	// URLs via the registry's forced routing (ADR-0023/0024 pattern).
	OptLuxForce = "lux"
)

// luxDomains lists the sites whose extractors are linked (curated subset).
var luxDomains = map[string]bool{
	"bilibili.com": true, "b23.tv": true, // bilibili short links
	"douyin.com":   true,
	"iqiyi.com":    true,
	"mgtv.com":     true,
	"ixigua.com":   true,
	"kuaishou.com": true,
}

// luxSiteHeaders carries the per-site request headers our HTTP engine must
// send when fetching the extracted media URLs. CDNs such as Bilibili's
// return 403 without the Referer; lux's Part struct carries no headers, so
// the site knowledge lives here (static, non-secret, no cookies).
var luxSiteHeaders = map[string]map[string]string{
	"bilibili.com": {"Referer": "https://www.bilibili.com/"},
	"b23.tv":       {"Referer": "https://www.bilibili.com/"},
	"mgtv.com":     {"Referer": "https://www.mgtv.com/"},
	"douyin.com":   {"Referer": "https://www.douyin.com/"},
	"iqiyi.com":    {"Referer": "https://www.iqiyi.com/"},
	"ixigua.com":   {"Referer": "https://www.ixigua.com/"},
	"kuaishou.com": {"Referer": "https://www.kuaishou.com/"},
}

// LuxResolver is the in-process site parser (ADR-0010 decision 1).
type LuxResolver struct {
	guard   core.URLGuard
	timeout time.Duration
	// extract is the offline-test seam; nil binds the real lux dispatch.
	extract func(u string, opt extractors.Options) ([]*extractors.Data, error)
}

// NewLuxResolver builds the resolver with the production extraction path.
func NewLuxResolver(g core.URLGuard, timeout time.Duration) *LuxResolver {
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	return &LuxResolver{guard: g, timeout: timeout}
}

// newLuxResolverWithExtract is the test seam: offline fixtures instead of
// the real network path.
func newLuxResolverWithExtract(g core.URLGuard, timeout time.Duration,
	extract func(u string, opt extractors.Options) ([]*extractors.Data, error)) *LuxResolver {
	l := NewLuxResolver(g, timeout)
	l.extract = extract
	return l
}

func (l *LuxResolver) Name() string { return "lux" }

// Match claims URLs whose host belongs to a linked extractor's site. lux maps
// hosts to extractors with its own domain logic, so the heuristic stays a
// superset-safe pre-filter: a miss here falls through to direct-http; a hit
// with no linked extractor fails cleanly inside Resolve.
func (l *LuxResolver) Match(in core.Input) bool {
	if in.Kind != core.InputURL {
		return false
	}
	host := luxHost(in.Raw)
	return host != "" && luxDomains[host]
}

// luxHost lower-cases and strips a leading "www." from the URL host.
func luxHost(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	host = strings.TrimPrefix(host, "www.")
	return host
}

// luxDomainCovered reports whether host belongs to a site whose extractor is
// linked: exact match or a subdomain of a curated domain (m.bilibili.com →
// bilibili.com). A bare suffix is not enough — "notbilibili.com" must not
// pass.
func luxDomainCovered(host string) bool {
	if host == "" {
		return false
	}
	if luxDomains[host] {
		return true
	}
	for d := range luxDomains {
		if strings.HasSuffix(host, "."+d) {
			return true
		}
	}
	return false
}

func (l *LuxResolver) Resolve(rc core.ResolveContext, in core.Input) (*core.Resource, error) {
	// Forced classification still requires http(s): the guard and the
	// transport both assume it (ADR-0024 pattern).
	if scheme := strings.ToLower(core.SchemeOf(in.Raw)); scheme != "http" && scheme != "https" {
		return nil, core.Err(core.E_INVALID_INPUT, "lux: only http(s) URLs are supported")
	}
	ctx := rc.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	if l.guard != nil {
		if err := l.guard.Check(ctx, in.Raw); err != nil {
			return nil, err
		}
	}

	// Pre-check the host against the curated extractor set BEFORE calling
	// lux: its dispatch falls back to extractorMap[""] which is nil, so an
	// unsupported site PANICS the library (found by the forced-routing test).
	// Fail cleanly instead — and never hand lux an out-of-list host.
	if host := luxHost(in.Raw); !luxDomainCovered(host) {
		return nil, core.Err(core.E_CAPABILITY_UNAVAILABLE,
			"site %q has no linked lux extractor (curated subset, ADR-0026)", host)
	}
	datas, err := l.extractBounded(ctx, in.Raw)
	if err != nil {
		return nil, err
	}
	data := pickVideoData(datas)
	if data == nil {
		return nil, core.Err(core.E_CAPABILITY_UNAVAILABLE,
			"lux extracted no usable video stream for this URL")
	}

	stream := pickStream(data.Streams)
	if stream == nil {
		return nil, core.Err(core.E_CAPABILITY_UNAVAILABLE,
			"lux returned no streams for %q", data.Title)
	}
	if len(stream.Parts) != 1 {
		// The scheduler downloads exactly Resource.Files[0]; multi-Part
		// streams need a concat pipeline that does not exist yet (P3).
		return nil, core.Err(core.E_CAPABILITY_UNAVAILABLE,
			"multi-part stream (%d parts) requires concatenation support (P3)", len(stream.Parts))
	}
	part := stream.Parts[0]
	if part.URL == "" {
		return nil, core.Err(core.E_RESOLVE_FAILED, "lux stream part has an empty URL")
	}
	if l.guard != nil {
		if err := l.guard.Check(ctx, part.URL); err != nil {
			return nil, err
		}
	}

	host := luxHost(in.Raw)
	name := sanitizeName(data.Title)
	if name == "download.bin" || name == "" {
		name = "video"
	}
	name += "." + sanitizeExt(stream.Ext)

	headers := map[string]string{}
	for k, v := range luxSiteHeaders[host] {
		headers[k] = v
	}

	return &core.Resource{
		ID:         "res-" + string(core.NewID()),
		Kind:       core.KindSingle,
		Engine:     core.EngineGo,
		Files:      []core.ResourceFile{{Index: 0, Path: name, Size: stream.Size, Selected: true}},
		Mirrors:    []core.Mirror{{URL: part.URL, Priority: 1, Weight: 1}},
		Headers:    headers,
		Credential: rc.Credential,
		Identity: core.ProtocolIdentity{
			VariantID:  "lux:" + stream.ID,
			IsAdaptive: true,
		},
		Limits: core.TaskLimits{},
	}, nil
}

// extractBounded runs the extraction with an abandon-on-timeout bound. lux's
// Extract is not context-aware, so the abandoned goroutine finishes in the
// background — bounded by the extraction itself, never by us.
func (l *LuxResolver) extractBounded(ctx context.Context, u string) ([]*extractors.Data, error) {
	extract := l.extract
	if extract == nil {
		extract = func(u string, opt extractors.Options) ([]*extractors.Data, error) {
			return extractors.Extract(u, opt)
		}
	}
	type result struct {
		datas []*extractors.Data
		err   error
	}
	ch := make(chan result, 1)
	go func() {
		// Site extractors parse hostile page content; a panic inside lux must
		// never take the daemon down (in-process extraction, ADR-0026).
		defer func() {
			if r := recover(); r != nil {
				ch <- result{err: fmt.Errorf("lux extractor panicked: %v", r)}
			}
		}()
		datas, err := extract(u, extractors.Options{Playlist: false})
		ch <- result{datas: datas, err: err}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			return nil, core.Err(core.E_TRANSIENT_NETWORK, "lux extraction failed: %v", r.err)
		}
		return r.datas, nil
	case <-time.After(l.timeout):
		return nil, core.Err(core.E_TRANSIENT_NETWORK,
			"lux extraction timed out after %s (the site may be slow or the extractor stale)", l.timeout)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// pickVideoData selects the first video entry without an extraction error.
func pickVideoData(datas []*extractors.Data) *extractors.Data {
	for _, d := range datas {
		if d == nil || d.Err != nil {
			continue
		}
		if d.Type == extractors.DataTypeVideo && len(d.Streams) > 0 {
			return d
		}
	}
	return nil
}

// pickStream selects the largest stream deterministically (Size desc, then
// stable ID asc for equal sizes) so replays and tests stay reproducible.
// The map key is the canonical stream ID: the real lux dispatch fills
// Stream.ID via FillUpStreamsData, but fixtures (and any extractor that
// forgets it) may leave it empty.
func pickStream(streams map[string]*extractors.Stream) *extractors.Stream {
	ids := make([]string, 0, len(streams))
	for id, s := range streams {
		if s == nil || len(s.Parts) == 0 {
			continue
		}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return nil
	}
	sort.Slice(ids, func(i, j int) bool {
		si, sj := streams[ids[i]], streams[ids[j]]
		if si.Size != sj.Size {
			return si.Size > sj.Size
		}
		return ids[i] < ids[j]
	})
	picked := streams[ids[0]]
	if picked.ID == "" {
		picked.ID = ids[0]
	}
	if picked.Quality == "" {
		picked.Quality = ids[0]
	}
	return picked
}

// sanitizeExt whitelists the merged-file extension the stream reports.
func sanitizeExt(ext string) string {
	ext = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(ext, ".")))
	switch ext {
	case "mp4", "mkv", "webm", "flv", "ts", "m4a", "mp3", "aac", "ogg", "jpg", "png":
		return ext
	default:
		return "mp4"
	}
}
