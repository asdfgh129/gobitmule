package resolve

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/asdfgh129/gobitmule/internal/core"
)

const ytdlpProgressiveJSON = `{
  "id": "abc123", "title": "A / B: Test",
  "formats": [
    {"format_id":"18", "url":"https://cdn.example/v/360.mp4", "ext":"mp4",
     "vcodec":"avc1", "acodec":"mp4a.40.2", "height":360, "tbr":800},
    {"format_id":"137", "url":"https://cdn.example/v/1080.mp4", "ext":"mp4",
     "vcodec":"avc1", "acodec":"mp4a.40.2", "height":1080, "tbr":5000,
     "http_headers":{"Referer":"https://www.youtube.com/"}}
  ]
}`

const ytdlpSeparatedJSON = `{
  "id":"sep", "title":"Separated", "formats":[
    {"format_id":"137","url":"https://cdn.example/v.mp4","ext":"mp4","vcodec":"avc1","acodec":"none","height":1080,"tbr":5000,"filesize":5000000,"http_headers":{"Referer":"https://www.youtube.com/","User-Agent":"test-agent"}},
    {"format_id":"140","url":"https://cdn.example/a.m4a","ext":"m4a","vcodec":"none","acodec":"mp4a.40.2","tbr":128,"filesize":128000,"http_headers":{"Referer":"https://www.youtube.com/"}}
  ]
}`

const ytdlpSeparatedNoSizeJSON = `{
  "id":"sepns", "title":"Separated No Size", "formats":[
    {"format_id":"137","url":"https://cdn.example/v.mp4","ext":"mp4","vcodec":"avc1","acodec":"none","height":1080,"tbr":5000},
    {"format_id":"140","url":"https://cdn.example/a.m4a","ext":"m4a","vcodec":"none","acodec":"mp4a.40.2","tbr":128}
  ]
}`

func ytdlpRunner(body string) func(context.Context, string, string) ([]byte, error) {
	return func(context.Context, string, string) ([]byte, error) { return []byte(body), nil }
}

func TestYTDLPMatch(t *testing.T) {
	y := NewYTDLP(nil, YTDLPOptions{})
	yes := []string{
		"https://www.youtube.com/watch?v=abc",
		"https://youtu.be/abc",
		"https://music.youtube.com/watch?v=abc",
		"https://vimeo.com/123",
	}
	for _, raw := range yes {
		if !y.Match(core.Input{Kind: core.InputURL, Raw: raw}) {
			t.Errorf("Match(%q) = false, want true", raw)
		}
	}
	no := []string{
		"https://unknown.example/video",
		"ftp://youtube.com/video",
		"magnet:?xt=urn:btih:abc",
	}
	for _, raw := range no {
		if y.Match(core.Input{Kind: core.InputURL, Raw: raw}) {
			t.Errorf("Match(%q) = true, want false", raw)
		}
	}
	if !y.Match(core.Input{Kind: core.InputSite, Raw: "https://example.org/watch/1"}) {
		t.Fatal("InputSite must be claimed when the sidecar resolver is enabled")
	}
}

func TestYTDLPResolveProgressive(t *testing.T) {
	g := &fakeGuard{}
	var gotURL, gotBinary string
	y := NewYTDLP(g, YTDLPOptions{Binary: "fake-ytdlp", Runner: func(_ context.Context, bin, raw string) ([]byte, error) {
		gotBinary, gotURL = bin, raw
		return []byte(ytdlpProgressiveJSON), nil
	}})
	in := core.Input{Kind: core.InputURL, Raw: "https://www.youtube.com/watch?v=abc"}
	res, err := y.Resolve(core.ResolveContext{}, in)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if gotBinary != "fake-ytdlp" || gotURL != in.Raw {
		t.Fatalf("runner args = (%q, %q)", gotBinary, gotURL)
	}
	if res.Kind != core.KindSingle || res.Engine != core.EngineGo || len(res.Files) != 1 {
		t.Fatalf("resource shape = %+v", res)
	}
	if res.Files[0].Path != "A _ B_ Test.mp4" {
		t.Fatalf("filename = %q", res.Files[0].Path)
	}
	if len(res.Mirrors) != 1 || res.Mirrors[0].URL != "https://cdn.example/v/1080.mp4" {
		t.Fatalf("mirrors = %+v", res.Mirrors)
	}
	if res.Headers["Referer"] != "https://www.youtube.com/" {
		t.Fatalf("headers = %+v", res.Headers)
	}
	if res.Identity.VariantID != ytdlpName || len(res.PostProcess) != 0 {
		t.Fatalf("identity/postprocess = %+v / %+v", res.Identity, res.PostProcess)
	}
	// Both the untrusted site URL and the chosen format URL must be guarded.
	if len(g.checked) != 2 || g.checked[0] != in.Raw || g.checked[1] != res.Mirrors[0].URL {
		t.Fatalf("guard checked %v", g.checked)
	}
}

func TestYTDLPResolveFailures(t *testing.T) {
	in := core.Input{Kind: core.InputURL, Raw: "https://www.youtube.com/watch?v=abc"}
	cases := []struct {
		name  string
		opt   YTDLPOptions
		guard *fakeGuard
		input core.Input
		code  string
	}{
		{name: "non-http site", opt: YTDLPOptions{Runner: ytdlpRunner(ytdlpProgressiveJSON)},
			input: core.Input{Kind: core.InputSite, Raw: "ytsearch:abc"}, code: core.E_INVALID_INPUT},
		{name: "bad JSON", opt: YTDLPOptions{Runner: ytdlpRunner("not json")}, input: in, code: core.E_RESOLVE_FAILED},
		{name: "oversized JSON", opt: YTDLPOptions{Runner: ytdlpRunner(ytdlpProgressiveJSON), MaxJSON: 16},
			input: in, code: core.E_INVALID_INPUT},
		{name: "separated without sizes", opt: YTDLPOptions{Runner: ytdlpRunner(ytdlpSeparatedNoSizeJSON)},
			input: in, code: core.E_CAPABILITY_UNAVAILABLE},
		{name: "no formats", opt: YTDLPOptions{Runner: ytdlpRunner(`{"id":"x","formats":[]}`)},
			input: in, code: core.E_RESOLVE_FAILED},
		{name: "guard rejection", opt: YTDLPOptions{Runner: ytdlpRunner(ytdlpProgressiveJSON)},
			guard: &fakeGuard{err: core.Err(core.E_INVALID_INPUT, "blocked")}, input: in, code: core.E_INVALID_INPUT},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := tc.guard
			if g == nil {
				g = &fakeGuard{}
			}
			y := NewYTDLP(g, tc.opt)
			_, err := y.Resolve(core.ResolveContext{}, tc.input)
			if err == nil {
				t.Fatal("expected failure")
			}
			if code := core.AsError(err).Code; code != tc.code {
				t.Fatalf("error code = %s, want %s (%v)", code, tc.code, err)
			}
		})
	}
}

func TestChooseFormatsPrefersProgressive(t *testing.T) {
	info := ytdlpInfo{Formats: []ytdlpFormat{
		{URL: "https://cdn.example/vid1080.mp4", Ext: "mp4", VCodec: "avc1", ACodec: "none", Height: 1080},
		{URL: "https://cdn.example/aud.mp4", Ext: "m4a", VCodec: "none", ACodec: "mp4a.40.2"},
		{URL: "https://cdn.example/prog720.mp4", Ext: "mp4", VCodec: "avc1", ACodec: "mp4a.40.2", Height: 720},
		{URL: "ftp://cdn.example/audio.ts", VCodec: "avc1", ACodec: "mp4a.40.2", Height: 2160},
	}}
	choice, err := ChooseFormats(info)
	if err != nil {
		t.Fatalf("ChooseFormats: %v", err)
	}
	if choice.Separate || len(choice.Files) != 1 || choice.Files[0].URL != "https://cdn.example/prog720.mp4" {
		t.Fatalf("choice = %+v", choice)
	}
}

func TestChooseFormatsTopLevelFallback(t *testing.T) {
	choice, err := ChooseFormats(ytdlpInfo{URL: "https://cdn.example/movie.mp4", Ext: "mp4"})
	if err != nil || len(choice.Files) != 1 || choice.Files[0].URL != "https://cdn.example/movie.mp4" {
		t.Fatalf("choice = %+v, err = %v", choice, err)
	}
	if _, err := ChooseFormats(ytdlpInfo{URL: "file:///etc/passwd"}); err == nil {
		t.Fatal("non-http top-level URL must be rejected")
	}
}

func TestYTDLPRealBinaryArguments(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell fixture")
	}
	dir := t.TempDir()
	fixture := filepath.Join(dir, "fixture.json")
	if err := os.WriteFile(fixture, []byte(ytdlpProgressiveJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$(dirname \"$0\")/args.txt\"\ncat \"" + fixture + "\"\n"
	bin := filepath.Join(dir, "fake-ytdlp")
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	y := NewYTDLP(nil, YTDLPOptions{Binary: bin})
	res, err := y.Resolve(core.ResolveContext{}, core.Input{Kind: core.InputURL, Raw: "https://youtu.be/abc"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if res.Mirrors[0].URL != "https://cdn.example/v/1080.mp4" {
		t.Fatalf("mirror = %q", res.Mirrors[0].URL)
	}
	b, err := os.ReadFile(filepath.Join(dir, "args.txt"))
	if err != nil {
		t.Fatal(err)
	}
	got := strings.TrimSpace(string(b))
	want := "-J\n--skip-download\n--no-warnings\n--no-playlist\n--\nhttps://youtu.be/abc"
	if got != want {
		t.Fatalf("sidecar args =\n%s\nwant\n%s", got, want)
	}
}

// ADR-0027: separated A/V formats with declared sizes become a two-file
// resource; the scheduler's postprocessor merges them (docs/06 §4.4).
func TestYTDLPSeparatedBecomesMultiFile(t *testing.T) {
	y := NewYTDLP(&fakeGuard{}, YTDLPOptions{Runner: ytdlpRunner(ytdlpSeparatedJSON)})
	res, err := y.Resolve(core.ResolveContext{}, core.Input{Kind: core.InputURL,
		Raw: "https://www.youtube.com/watch?v=sep"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(res.Files) != 2 {
		t.Fatalf("files = %d, want 2", len(res.Files))
	}
	v, a := res.Files[0], res.Files[1]
	if v.URL != "https://cdn.example/v.mp4" || v.Size != 5000000 {
		t.Fatalf("video file = %+v", v)
	}
	if a.URL != "https://cdn.example/a.m4a" || a.Size != 128000 {
		t.Fatalf("audio file = %+v", a)
	}
	if !strings.HasSuffix(v.Path, ".mp4") {
		t.Fatalf("final name = %q, want the merged .mp4", v.Path)
	}
	if res.Headers == nil {
		t.Fatal("merged site headers missing")
	}
}
