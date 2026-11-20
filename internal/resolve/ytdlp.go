package resolve

// WBS 12: opt-in yt-dlp sidecar resolver (ADR-0010). The sidecar is invoked
// with `-J --skip-download` and ONLY returns JSON; it never downloads a byte
// or writes a final file. Every format URL is validated by the SSRF Guard
// before it enters a Resource, and format selection is ours so a missing
// ffmpeg does not degrade YouTube to progressive 720p.
//
// Scope note (2026-11-20): the scheduler moves exactly one Resource file per
// task. This resolver therefore emits the best PROGRESSIVE (video+audio)
// format. A format list containing only separated video/audio streams fails
// closed with E_CAPABILITY_UNAVAILABLE instead of silently producing a
// video-only or audio-only file. Multi-file merging is a separate P3 item.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/asdfgh129/ud/internal/core"
)

const (
	ytdlpName    = "yt-dlp"
	maxYTDLPJSON = 16 << 20
)

// YTDLPOptions configures the sidecar. Timeout/binary come from config; the
// runner seam exists for fixture-based tests (no network, no Python required).
type YTDLPOptions struct {
	Binary  string
	Timeout time.Duration
	MaxJSON int
	// Runner, when set, replaces exec.CommandContext. It receives the URL and
	// returns the JSON that yt-dlp -J would have printed.
	Runner func(ctx context.Context, binary, rawURL string) ([]byte, error)
}

type YTDLP struct {
	guard core.URLGuard
	opt   YTDLPOptions
}

func NewYTDLP(g core.URLGuard, opt YTDLPOptions) *YTDLP {
	if strings.TrimSpace(opt.Binary) == "" {
		opt.Binary = "yt-dlp"
	}
	if opt.Timeout <= 0 {
		opt.Timeout = 2 * time.Minute
	}
	if opt.MaxJSON <= 0 {
		opt.MaxJSON = maxYTDLPJSON
	}
	return &YTDLP{guard: g, opt: opt}
}

func (y *YTDLP) Name() string { return ytdlpName }

// siteHosts is a conservative allowlist: only these hosts invoke the sidecar.
// Explicit InputSite always matches (caller knows it is a content source).
var siteHosts = []string{
	"youtube.com", "youtu.be", "m.youtube.com", "www.youtube.com",
	"music.youtube.com", "vimeo.com", "www.vimeo.com",
	"dailymotion.com", "www.dailymotion.com", "twitch.tv", "www.twitch.tv",
	"twitter.com", "x.com", "tiktok.com", "www.tiktok.com",
	"soundcloud.com", "bandcamp.com",
}

func (y *YTDLP) Match(in core.Input) bool {
	if in.Kind == core.InputSite {
		return true
	}
	if in.Kind != core.InputURL {
		return false
	}
	host, ok := ytdlpHost(in.Raw)
	if !ok {
		return false
	}
	for _, suffix := range siteHosts {
		if host == suffix || strings.HasSuffix(host, "."+suffix) {
			return true
		}
	}
	return false
}

func ytdlpHost(raw string) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return "", false
	}
	return strings.ToLower(u.Hostname()), true
}

type ytdlpFormat struct {
	FormatID    string            `json:"format_id"`
	URL         string            `json:"url"`
	Ext         string            `json:"ext"`
	Protocol    string            `json:"protocol"`
	VCodec      string            `json:"vcodec"`
	ACodec      string            `json:"acodec"`
	Width       int               `json:"width"`
	Height      int               `json:"height"`
	FPS         float64           `json:"fps"`
	TBR         float64           `json:"tbr"`
	Filesize    int64             `json:"filesize"`
	ApproxSize  int64             `json:"filesize_approx"`
	HTTPHeaders map[string]string `json:"http_headers"`
}

type ytdlpInfo struct {
	ID          string            `json:"id"`
	Title       string            `json:"title"`
	Duration    float64           `json:"duration"`
	URL         string            `json:"url"`
	Ext         string            `json:"ext"`
	VCodec      string            `json:"vcodec"`
	ACodec      string            `json:"acodec"`
	Width       int               `json:"width"`
	Height      int               `json:"height"`
	Formats     []ytdlpFormat     `json:"formats"`
	HTTPHeaders map[string]string `json:"http_headers"`
	Subtitles   map[string]any    `json:"subtitles"`
}

// Resolve calls the sidecar and maps the chosen progressive format onto a
// single-file Resource. Multi-file (separated A/V) plans fail closed; see the
// scope note at the top of this file.
func (y *YTDLP) Resolve(ctx core.ResolveContext, in core.Input) (*core.Resource, error) {
	cctx := ctx.Ctx
	if cctx == nil {
		cctx = context.Background()
	}
	if _, ok := ytdlpHost(in.Raw); !ok {
		return nil, core.Err(core.E_INVALID_INPUT, "yt-dlp accepts only http(s) URLs with a host")
	}
	if err := y.guardSiteURL(cctx, in.Raw); err != nil {
		return nil, err
	}
	raw, err := y.run(cctx, in.Raw)
	if err != nil {
		return nil, err
	}
	if len(raw) > y.opt.MaxJSON {
		return nil, core.Err(core.E_INVALID_INPUT, "yt-dlp JSON exceeds %d bytes", y.opt.MaxJSON)
	}
	var info ytdlpInfo
	if err := json.NewDecoder(bytes.NewReader(raw)).Decode(&info); err != nil {
		return nil, core.Err(core.E_RESOLVE_FAILED, "yt-dlp JSON invalid: %v", err)
	}
	choice, err := ChooseFormats(info)
	if err != nil {
		return nil, err
	}
	if choice.Separate {
		return nil, core.Err(core.E_CAPABILITY_UNAVAILABLE,
			"yt-dlp returned only separated A/V formats; progressive merging is not implemented (ADR-0010)")
	}
	f := choice.Files[0]
	if err := y.guardSiteURL(cctx, f.URL); err != nil {
		return nil, err
	}
	name := safeSiteName(info.Title, info.ID)
	if f.Ext != "" {
		name += "." + f.Ext
	}
	return &core.Resource{
		ID:         "res-" + string(core.NewID()),
		Kind:       core.KindSingle,
		Engine:     core.EngineGo,
		Files:      []core.ResourceFile{{Index: 0, Path: name, Selected: true}},
		Mirrors:    []core.Mirror{{URL: f.URL, Priority: 1, Weight: 1}},
		Headers:    f.Headers,
		Credential: ctx.Credential,
		Identity: core.ProtocolIdentity{
			VariantID: ytdlpName,
		},
	}, nil
}

func (y *YTDLP) guardSiteURL(ctx context.Context, raw string) error {
	if y.guard == nil {
		return nil
	}
	return y.guard.Check(ctx, raw)
}

func (y *YTDLP) run(ctx context.Context, rawURL string) ([]byte, error) {
	if y.opt.Runner != nil {
		return y.opt.Runner(ctx, y.opt.Binary, rawURL)
	}
	cctx, cancel := context.WithTimeout(ctx, y.opt.Timeout)
	defer cancel()
	// Allowlisted arguments only: the validated URL is a single positional
	// argument after `--`, never interpreted as a flag (ADR-0008 / docs/09 §7).
	cmd := exec.CommandContext(cctx, y.opt.Binary,
		"-J", "--skip-download", "--no-warnings", "--no-playlist", "--", rawURL)
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	cmd.Env = sidecarEnv()
	if err := cmd.Run(); err != nil {
		return nil, core.Err(core.E_CAPABILITY_UNAVAILABLE,
			"yt-dlp metadata extraction failed: %v: %s", err, tail(errBuf.String(), 512))
	}
	if out.Len() > y.opt.MaxJSON {
		return nil, core.Err(core.E_INVALID_INPUT, "yt-dlp JSON exceeds %d bytes", y.opt.MaxJSON)
	}
	return out.Bytes(), nil
}

// sidecarEnv passes only the lookup/paths a Python sidecar needs. Inheriting
// the daemon's whole environment would leak tokens into an external process.
func sidecarEnv() []string {
	env := []string{"PATH=" + defaultYTDLPEnvPath()}
	for _, key := range []string{"HOME", "USERPROFILE", "TMPDIR", "TMP", "TEMP", "SystemRoot", "LANG"} {
		if v := os.Getenv(key); v != "" {
			env = append(env, key+"="+v)
		}
	}
	return env
}

func defaultYTDLPEnvPath() string {
	if v := os.Getenv("PATH"); v != "" {
		return v
	}
	return "/usr/local/bin:/usr/bin:/bin"
}

// FormatChoice is the result of our own selection policy.
type FormatChoice struct {
	Files    []chosenFile
	Separate bool
}

type chosenFile struct {
	URL     string
	Ext     string
	Headers map[string]string
}

// ChooseFormats implements ADR-0010 §4: prefer the highest-resolution
// progressive H.264/AAC format, because a missing ffmpeg must not degrade the
// download to yt-dlp's 720p default. Separated formats are returned as a
// two-file plan so callers can fail explicitly until multi-file merge lands.
func ChooseFormats(info ytdlpInfo) (FormatChoice, error) {
	if len(info.Formats) == 0 {
		f := ytdlpFormat{URL: info.URL, Ext: info.Ext, VCodec: info.VCodec,
			ACodec: info.ACodec, Width: info.Width, Height: info.Height,
			HTTPHeaders: info.HTTPHeaders}
		if !validFormatURL(f.URL) {
			return FormatChoice{}, core.Err(core.E_RESOLVE_FAILED, "yt-dlp returned no usable formats")
		}
		return FormatChoice{Files: []chosenFile{toChosen(f)}}, nil
	}
	var progressive, video, audio []ytdlpFormat
	for _, f := range info.Formats {
		if !validFormatURL(f.URL) {
			continue
		}
		hasVideo := f.VCodec != "" && !strings.EqualFold(f.VCodec, "none")
		hasAudio := f.ACodec != "" && !strings.EqualFold(f.ACodec, "none")
		switch {
		case hasVideo && hasAudio:
			progressive = append(progressive, f)
		case hasVideo:
			video = append(video, f)
		case hasAudio:
			audio = append(audio, f)
		}
	}
	if p := bestFormat(progressive); p.URL != "" {
		return FormatChoice{Files: []chosenFile{toChosen(p)}}, nil
	}
	v, a := bestFormat(video), bestFormat(audio)
	if v.URL != "" && a.URL != "" {
		return FormatChoice{
			Files:    []chosenFile{toChosen(v), toChosen(a)},
			Separate: true,
		}, nil
	}
	return FormatChoice{}, core.Err(core.E_RESOLVE_FAILED, "yt-dlp returned no usable formats")
}

func validFormatURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Host != "" && (u.Scheme == "http" || u.Scheme == "https") && u.User == nil
}

func bestFormat(list []ytdlpFormat) ytdlpFormat {
	var out ytdlpFormat
	for _, f := range list {
		if out.URL == "" || score(f) > score(out) {
			out = f
		}
	}
	return out
}

func score(f ytdlpFormat) int64 {
	// Height dominates, then bitrate; H.264/AAC get a preference so the
	// built-in Remux can merge future separated streams without ffmpeg.
	// A filesize hint breaks ties deterministically.
	s := int64(f.Height)*100000 + int64(f.TBR)*100
	vc, ac := strings.ToLower(f.VCodec), strings.ToLower(f.ACodec)
	if strings.Contains(vc, "avc") || strings.Contains(vc, "h264") {
		s += 5000
	}
	if strings.Contains(ac, "aac") || strings.Contains(ac, "mp4a") {
		s += 1000
	}
	if f.ApproxSize > 0 {
		s += f.ApproxSize / (1 << 20)
	} else if f.Filesize > 0 {
		s += f.Filesize / (1 << 20)
	}
	return s
}

func toChosen(f ytdlpFormat) chosenFile {
	return chosenFile{URL: f.URL, Ext: f.Ext, Headers: f.HTTPHeaders}
}

func safeSiteName(title, id string) string {
	s := strings.TrimSpace(title)
	if s == "" {
		s = id
	}
	if s == "" {
		s = "site-download"
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_', r == ' ', r >= 0x4e00 && r <= 0x9fff:
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := strings.TrimSpace(b.String())
	if out == "" {
		return "site-download"
	}
	if len(out) > 120 {
		out = out[:120]
	}
	return out
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
