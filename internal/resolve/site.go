package resolve

import (
	"context"
	"net/url"
	"sort"
	"strings"

	"github.com/asdfgh129/ud/internal/core"
)

// ============================================================================
// Lux Resolver: Chinese video sites (in-process, goja plugin rules)
// ============================================================================

const (
	OptLuxForce    = "lux"
	OptLuxPlugin   = "lux_plugin"   // optional: custom plugin rule pack path
	OptLuxCacheDir = "lux_cache_dir" // optional: rule cache directory
)

// LuxResolver handles Chinese video sites (Bilibili, Youku, etc.) using
// the lux library with goja plugin rules. Pure Go, zero sidecar.
type LuxResolver struct {
	guard    core.URLGuard
	plugin   string
	cacheDir string
}

// NewLuxResolver builds the lux resolver.
func NewLuxResolver(g core.URLGuard, opts map[string]string) *LuxResolver {
	plugin := ""
	cacheDir := ""
	if opts != nil {
		plugin = opts[OptLuxPlugin]
		cacheDir = opts[OptLuxCacheDir]
	}
	return &LuxResolver{guard: g, plugin: plugin, cacheDir: cacheDir}
}

func (l *LuxResolver) Name() string { return "lux" }

func (l *LuxResolver) Match(in core.Input) bool {
	if in.Kind != core.InputURL {
		return false
	}
	// Heuristic: known Chinese video domains
	return isKnownLuxDomain(in.Raw)
}

func isKnownLuxDomain(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	// Common Chinese video platforms supported by lux
	known := map[string]bool{
		"bilibili.com":        true,
		"www.bilibili.com":    true,
		"youku.com":           true,
		"www.youku.com":       true,
		"iqiyi.com":           true,
		"www.iqiyi.com":       true,
		"tencent.com":         true, // v.qq.com
		"v.qq.com":            true,
		"mgtv.com":            true,
		"www.mgtv.com":        true,
		"douyin.com":          true,
		"www.douyin.com":      true,
		"kuaishou.com":        true,
		"www.kuaishou.com":    true,
		"acfun.cn":            true,
		"www.acfun.cn":        true,
		"xigua.com":           true,
		"www.xigua.com":       true,
		"pipix.com":           true,
		"www.pipix.com":       true,
		"haokan.baidu.com":    true,
		"weibo.com":           true,
		"www.weibo.com":       true,
		"sohu.com":            true,
		"tv.sohu.com":         true,
		"pptv.com":            true,
		"1905.com":            true,
		"fun.tv":              true,
		"ixigua.com":          true,
	}
	return known[host]
}

func (l *LuxResolver) Resolve(ctx core.ResolveContext, in core.Input) (*core.Resource, error) {
	if l.guard != nil {
		c := ctx.Ctx
		if c == nil {
			c = context.Background()
		}
		if err := l.guard.Check(c, in.Raw); err != nil {
			return nil, err
		}
	}
	// Forced classification via ResolveContext.Options (ADR-0023/0024 pattern)
	if force, ok := ctx.Options[OptLuxForce].(bool); ok && force {
		scheme := strings.ToLower(urlScheme(in.Raw))
		if scheme != "http" && scheme != "https" {
			return nil, core.Err(core.E_INVALID_INPUT, "lux: only http(s) URLs are supported")
		}
	}

	// TODO: actual lux integration when library available
	// For now, construct a minimal Resource with the URL passed through
	// The actual parsing would call lux.Extract() and return format info
	return &core.Resource{
		ID:     "res-" + string(core.NewID()),
		Kind:   core.KindSingle,
		Engine: core.EngineGo,
		Files:  []core.ResourceFile{{Index: 0, Path: "lux_parsed", Selected: true}},
		Mirrors: []core.Mirror{{URL: in.Raw, Priority: 1}},
		Headers:    map[string]string{},
		Credential: ctx.Credential,
		Identity: core.ProtocolIdentity{
			VariantID:  "lux",
			IsAdaptive: true,
		},
		Limits: core.TaskLimits{},
	}, nil
}

// ============================================================================
// YtDlpResolver: YouTube & long-tail sites via yt-dlp sidecar
// ============================================================================

const (
	OptYtDlpForce    = "yt-dlp"
	OptYtDlpPath     = "yt_dlp_path"     // optional: path to yt-dlp binary
	OptDenoPath      = "deno_path"       // optional: path to deno binary
	OptFormatSelect  = "format_select"   // optional: format selection policy JSON
	OptYtDlpCacheDir = "yt_dlp_cache_dir" // optional: yt-dlp cache directory
)

// YtDlpResolver handles YouTube and long-tail sites via yt-dlp sidecar.
// yt-dlp runs as a separate process: `yt-dlp -J --skip-download <URL>`.
// Output is parsed JSON with format list; format selection happens here.
type YtDlpResolver struct {
	guard       core.URLGuard
	ytDlpPath   string
	denoPath    string
	cacheDir    string
	formatSelect string // JSON format selection policy
}

// NewYtDlpResolver builds the yt-dlp resolver.
func NewYtDlpResolver(g core.URLGuard, opts map[string]string) *YtDlpResolver {
	ytDlpPath := "yt-dlp"
	denoPath := "deno"
	cacheDir := ""
	formatSelect := ""
	if opts != nil {
		if v, ok := opts[OptYtDlpPath]; ok && v != "" {
			ytDlpPath = v
		}
		if v, ok := opts[OptDenoPath]; ok && v != "" {
			denoPath = v
		}
		if v, ok := opts[OptYtDlpCacheDir]; ok && v != "" {
			cacheDir = v
		}
		if v, ok := opts[OptFormatSelect]; ok && v != "" {
			formatSelect = v
		}
	}
	return &YtDlpResolver{guard: g, ytDlpPath: ytDlpPath, denoPath: denoPath, cacheDir: cacheDir, formatSelect: formatSelect}
}

func (y *YtDlpResolver) Name() string { return "yt-dlp" }

func (y *YtDlpResolver) Match(in core.Input) bool {
	if in.Kind != core.InputURL {
		return false
	}
	// Known yt-dlp domains: YouTube and common long-tail
	return isKnownYtDlpDomain(in.Raw)
}

func isKnownYtDlpDomain(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	// YouTube and common yt-dlp sites
	known := map[string]bool{
		"youtube.com":     true,
		"www.youtube.com": true,
		"youtu.be":        true,
		"m.youtube.com":   true,
		"music.youtube.com": true,
		"vimeo.com":       true,
		"www.vimeo.com":   true,
		"dailymotion.com": true,
		"www.dailymotion.com": true,
		"twitch.tv":       true,
		"www.twitch.tv":   true,
		"twitter.com":     true,
		"x.com":           true,
		"facebook.com":    true,
		"fb.watch":        true,
		"instagram.com":   true,
		"www.instagram.com": true,
		"tiktok.com":      true,
		"www.tiktok.com":  true,
		"soundcloud.com":  true,
		"bandcamp.com":    true,
		"github.com":      true, // raw files via yt-dlp
	}
	return known[host]
}

func (y *YtDlpResolver) Resolve(ctx core.ResolveContext, in core.Input) (*core.Resource, error) {
	// Forced classification via ResolveContext.Options
	if force, ok := ctx.Options[OptYtDlpForce].(bool); ok && force {
		scheme := strings.ToLower(urlScheme(in.Raw))
		if scheme != "http" && scheme != "https" {
			return nil, core.Err(core.E_INVALID_INPUT, "yt-dlp: only http(s) URLs are supported")
		}
	}

	// yt-dlp resolution is async and may need Deno for YouTube EJS/PO tokens
	// The actual extraction happens via the yt-dlp sidecar process.
	// For now, return a placeholder Resource; actual extraction happens in transport.
	return &core.Resource{
		ID:     "res-" + string(core.NewID()),
		Kind:   core.KindSingle,
		Engine: core.EngineGo,
		Files:  []core.ResourceFile{{Index: 0, Path: "yt_dlp_parsed", Selected: true}},
		Mirrors: []core.Mirror{{URL: in.Raw, Priority: 1}},
		Headers:    map[string]string{},
		Credential: ctx.Credential,
		Identity: core.ProtocolIdentity{
			VariantID:  "yt-dlp",
			IsAdaptive: true,
		},
		Limits: core.TaskLimits{},
	}, nil
}

// ============================================================================
// Format Selection: shared logic for yt-dlp format list
// ============================================================================

// YtDlpFormat represents one format from yt-dlp -J output.
type YtDlpFormat struct {
	FormatID      string  `json:"format_id"`
	Ext           string  `json:"ext"`
	Resolution    string  `json:"resolution"`
	Width         int     `json:"width"`
	Height        int     `json:"height"`
	FPS           float64 `json:"fps"`
	VCodec        string  `json:"vcodec"`
	ACodec        string  `json:"acodec"`
	VideoExt      string  `json:"video_ext"`
	AudioExt      string  `json:"audio_ext"`
	Protocol      string  `json:"protocol"`
	URL           string  `json:"url"`
	TBR           float64 `json:"tbr"` // total bitrate
	VBR           float64 `json:"vbr"`
	ABR           float64 `json:"abr"`
	Filesize      int64   `json:"filesize"`
	FilesizeApprox int64  `json:"filesize_approx"`
	DynamicRange  string  `json:"dynamic_range"`
	Quality       string  `json:"quality"`
	FormatNote    string  `json:"format_note"`
	Container     string  `json:"container"`
	Downloader    string  `json:"downloader"`
}

// FormatSelectPolicy controls format selection from yt-dlp format list.
type FormatSelectPolicy struct {
	PreferCodec     string  `json:"prefer_codec"`     // e.g., "av01", "avc1", "vp9"
	PreferContainer string  `json:"prefer_container"` // e.g., "mp4", "webm"
	MaxHeight       int     `json:"max_height"`       // e.g., 1080, 4320
	MaxBitrate      float64 `json:"max_bitrate"`      // Mbps
	PreferAV1       bool    `json:"prefer_av1"`
	PreferVP9       bool    `json:"prefer_vp9"`
	PreferH264      bool    `json:"prefer_h264"`
	PreferAAC       bool    `json:"prefer_aac"`
	PreferOpus      bool    `json:"prefer_opus"`
	MergeVideoAudio bool    `json:"merge_video_audio"` // whether to merge separate video+audio
}

func DefaultFormatSelectPolicy() FormatSelectPolicy {
	return FormatSelectPolicy{
		PreferCodec:     "av01",
		PreferContainer: "mp4",
		MaxHeight:       4320,
		MaxBitrate:      50, // Mbps
		PreferAV1:       true,
		PreferVP9:       true,
		PreferH264:      false,
		PreferAAC:       false,
		PreferOpus:      true,
		MergeVideoAudio: true,
	}
}

// SelectBestFormat picks the best format from yt-dlp's format list per policy.
func SelectBestFormat(formats []YtDlpFormat, policy FormatSelectPolicy) *YtDlpFormat {
	if len(formats) == 0 {
		return nil
	}
	// Filter by constraints
	candidates := make([]YtDlpFormat, 0, len(formats))
	for _, f := range formats {
		if policy.MaxHeight > 0 && f.Height > policy.MaxHeight {
			continue
		}
		if policy.MaxBitrate > 0 && f.TBR > policy.MaxBitrate {
			continue
		}
		candidates = append(candidates, f)
	}
	if len(candidates) == 0 {
		return nil
	}
	// Score and sort
	type scoredItem struct {
		fmt  YtDlpFormat
		score float64
	}
	scored := make([]scoredItem, len(candidates))
	for i, f := range candidates {
		s := 0.0
		// Codec preference
		if policy.PreferAV1 && strings.Contains(f.VCodec, "av01") { s += 1000 }
		if policy.PreferVP9 && strings.Contains(f.VCodec, "vp9") { s += 500 }
		if policy.PreferH264 && strings.Contains(f.VCodec, "avc1") { s += 200 }
		if policy.PreferAV1 && strings.Contains(f.ACodec, "opus") { s += 100 }
		if policy.PreferAAC && strings.Contains(f.ACodec, "aac") { s += 50 }
		if policy.PreferOpus && strings.Contains(f.ACodec, "opus") { s += 100 }
		// Container preference
		if policy.PreferContainer != "" && strings.EqualFold(f.Ext, policy.PreferContainer) { s += 200 }
		// Resolution (higher is better up to max)
		if f.Height > 0 { s += float64(f.Height) * 0.1 }
		// Bitrate
		s += f.TBR * 0.01
		scored[i] = scoredItem{fmt: f, score: s}
	}
	// Sort by score descending
	sort.Slice(scored, func(i, j int) bool { return scored[i].score > scored[j].score })
	return &scored[0].fmt
}

// ============================================================================
// Helper functions
// ============================================================================

func urlScheme(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Scheme
}

