package dashtransport

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/asdfgh129/gobitmule/internal/config"
	"github.com/asdfgh129/gobitmule/internal/core"
	"github.com/asdfgh129/gobitmule/internal/security"
)

// testWriter is the scheduler-owned ChunkWriter stand-in (same shape as the
// ftp/hls test doubles): a fixed chunk plan over one file.
type testWriter struct {
	mu       sync.Mutex
	chunks   []core.Chunk
	written  map[int][]byte
	released map[int]error
}

func newTestWriter(size, chunkSize int64) *testWriter {
	var chunks []core.Chunk
	for start, idx := int64(0), 0; start < size; start, idx = start+chunkSize, idx+1 {
		n := chunkSize
		if size-start < chunkSize {
			n = size - start
		}
		chunks = append(chunks, core.Chunk{
			Index: idx, Start: start, Length: n, State: core.ChunkPending,
		})
	}
	return &testWriter{chunks: chunks, written: map[int][]byte{}, released: map[int]error{}}
}

func (w *testWriter) Next(context.Context) (core.Chunk, bool, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for i := range w.chunks {
		if w.chunks[i].State == core.ChunkPending {
			w.chunks[i].State = core.ChunkAssigned
			return w.chunks[i], true, nil
		}
	}
	return core.Chunk{}, false, nil
}

func (w *testWriter) WriteChunk(_ context.Context, c core.Chunk, p []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if int64(len(p)) != c.Length {
		return fmt.Errorf("chunk %d byte count mismatch: want %d got %d", c.Index, c.Length, len(p))
	}
	w.written[c.Index] = append([]byte(nil), p...)
	for i := range w.chunks {
		if w.chunks[i].Index == c.Index {
			w.chunks[i].State = core.ChunkDone
		}
	}
	return nil
}

func (w *testWriter) Verified(context.Context, core.Chunk, core.Hash) error { return nil }

func (w *testWriter) Release(_ context.Context, c core.Chunk, err error) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.released[c.Index] = err
	for i := range w.chunks {
		if w.chunks[i].Index == c.Index {
			w.chunks[i].State = core.ChunkPending
		}
	}
	return nil
}

func (w *testWriter) markDoneUpTo(nChunks int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for i := 0; i < nChunks && i < len(w.chunks); i++ {
		w.chunks[i].State = core.ChunkDone
	}
}

func (w *testWriter) concatenation() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	var out []byte
	for i := 0; i < len(w.chunks); i++ {
		out = append(out, w.written[i]...)
	}
	return out
}

type countingBudget struct {
	mu    sync.Mutex
	bytes int
}

func (b *countingBudget) Consume(_ context.Context, n int) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.bytes += n
	return nil
}

func (b *countingBudget) charged() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.bytes
}

func testGuard(t *testing.T) *security.SSRFGuard {
	t.Helper()
	return security.NewSSRFGuard(config.SecurityConfig{
		AllowedSchemes:       []string{"http", "https"},
		AllowedPorts:         []int{80, 443},
		MaxRedirects:         3,
		BlockPrivateNetworks: true,
		AllowLoopbackForDev:  true,
	})
}

// dashServer serves a static MPD plus its init / segment files. Segment
// bodies are opaque bytes: fMP4 concatenation does not inspect the payload,
// so the fixture does not need real boxes.
type dashServer struct {
	*httptest.Server
	mu          sync.Mutex
	init        []byte
	segments    [][]byte
	failSegment int // -1 = none; index into segments
	requested   []string
}

func newDASHServer(t *testing.T, init []byte, segments [][]byte, mode string) *dashServer {
	t.Helper()
	s := &dashServer{init: init, segments: segments, failSegment: -1}
	mux := http.NewServeMux()
	s.record(mux)
	var mediaTpl string
	var mpdBody string
	switch mode {
	case "number":
		mediaTpl = "seg-$Number%05d$.m4s"
	case "list":
		mediaTpl = ""
	case "plain":
		mediaTpl = "seg-$Number$.m4s"
	}
	var templateEl string
	if mode == "list" {
		var b strings.Builder
		b.WriteString(`<SegmentList timescale="1000" duration="1000"><Initialization sourceURL="init.m4s"/>`)
		for i := range segments {
			fmt.Fprintf(&b, `<SegmentURL media="segment-%d.m4s"/>`, i)
		}
		b.WriteString(`</SegmentList>`)
		templateEl = b.String()
	} else if mode == "number" {
		templateEl = fmt.Sprintf(`<SegmentTemplate timescale="1" duration="1" startNumber="0" initialization="init.m4s" media="%s"/>`, mediaTpl)
	} else {
		templateEl = fmt.Sprintf(`<SegmentTemplate timescale="1" duration="1" startNumber="0" initialization="init.m4s" media="%s"/>`, mediaTpl)
	}
	mpdBody = fmt.Sprintf(`<?xml version="1.0"?>
<MPD type="static" mediaPresentationDuration="PT%dS" profiles="urn:mpeg:dash:profile:isoff-live:2012">
  <Period>
    <AdaptationSet contentType="video" mimeType="video/mp4">
      <Representation id="v1" bandwidth="1000000">%s</Representation>
    </AdaptationSet>
  </Period>
</MPD>`, len(segments), templateEl)
	mux.HandleFunc("/manifest.mpd", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(mpdBody))
	})
	mux.HandleFunc("/dynamic.mpd", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<?xml version="1.0"?><MPD type="dynamic"><Period><AdaptationSet><Representation id="v" bandwidth="1"><SegmentTemplate media="s$Number$.m4s" duration="1"/></Representation></AdaptationSet></Period></MPD>`))
	})
	mux.HandleFunc("/drm.mpd", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<?xml version="1.0"?><MPD type="static"><Period><AdaptationSet><ContentProtection schemeIdUri="urn:mpeg:dash:mp4-protection:2011"/><Representation id="v" bandwidth="1"><SegmentTemplate media="s$Number$.m4s" duration="1"/></Representation></AdaptationSet></Period></MPD>`))
	})
	mux.HandleFunc("/multi.mpd", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<?xml version="1.0"?><MPD type="static"><Period>
			<AdaptationSet contentType="video"><Representation id="v" bandwidth="1"><SegmentTemplate media="v$Number$.m4s" duration="1"/></Representation></AdaptationSet>
			<AdaptationSet contentType="audio"><Representation id="a" bandwidth="1"><SegmentTemplate media="a$Number$.m4s" duration="1"/></Representation></AdaptationSet>
		</Period></MPD>`))
	})
	mux.HandleFunc("/init.m4s", func(w http.ResponseWriter, r *http.Request) {
		if serveRange(w, r, s.init) {
			return
		}
		_, _ = w.Write(s.init)
	})
	for i, body := range s.segments {
		seg, idx := body, i
		// The handler path must match what the mode's template expands to:
		// "number" uses %05d formatting, "list" uses the SegmentURL names.
		var pattern string
		switch mode {
		case "number":
			pattern = fmt.Sprintf("/seg-%05d.m4s", i)
		case "list":
			pattern = fmt.Sprintf("/segment-%d.m4s", i)
		default:
			pattern = fmt.Sprintf("/seg-%d.m4s", i)
		}
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			s.noteRequest(r.URL.Path)
			s.mu.Lock()
			fail := s.failSegment == idx
			s.mu.Unlock()
			if fail {
				http.Error(w, "synthetic failure", http.StatusNotFound)
				return
			}
			if serveRange(w, r, seg) {
				return
			}
			_, _ = w.Write(seg)
		})
	}
	s.Server = httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s
}

// record logs segment requests so tests can assert the expanded template URLs.
func (s *dashServer) record(mux *http.ServeMux) {
	orig := mux.Handler
	_ = orig
	// Wrap via a global recorder on each segment handler instead: keep it
	// simple by recording inside serveSegment below.
}

func (s *dashServer) noteRequest(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requested = append(s.requested, path)
}

// serveRange answers the 1-byte probe with the on-wire total.
func serveRange(w http.ResponseWriter, r *http.Request, body []byte) bool {
	if r.Header.Get("Range") != probeRange {
		return false
	}
	w.Header().Set("Content-Range", fmt.Sprintf("bytes 0-0/%d", len(body)))
	w.WriteHeader(http.StatusPartialContent)
	_, _ = w.Write(body[:1])
	return true
}

func streamResource(url string) *core.Resource {
	return &core.Resource{
		ID:      "res-dash",
		Kind:    core.KindStream,
		Engine:  core.EngineGo,
		Files:   []core.ResourceFile{{Index: 0, Path: "manifest.mp4", Selected: true}},
		Mirrors: []core.Mirror{{URL: url}},
	}
}

func allBytes(init []byte, segs [][]byte) []byte {
	var out []byte
	out = append(out, init...)
	for _, s := range segs {
		out = append(out, s...)
	}
	return out
}

func totalBytes(init []byte, segs [][]byte) int64 {
	n := int64(len(init))
	for _, s := range segs {
		n += int64(len(s))
	}
	return n
}

func TestProbeSumsInitAndSegmentSizes(t *testing.T) {
	init := bytes.Repeat([]byte{0x66}, 64)
	segs := [][]byte{bytes.Repeat([]byte{0x41}, 1000), bytes.Repeat([]byte{0x42}, 2500), bytes.Repeat([]byte{0x43}, 17)}
	srv := newDASHServer(t, init, segs, "number")
	tr := New(nil)
	p, err := tr.Probe(context.Background(), streamResource(srv.URL+"/manifest.mpd"))
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if p.Size != totalBytes(init, segs) {
		t.Fatalf("size = %d, want %d", p.Size, totalBytes(init, segs))
	}
	if !p.AcceptRanges || p.Filename != "manifest.mp4" {
		t.Fatalf("probe = %+v", p)
	}
}

func TestRunConcatenatesInitAndSegments(t *testing.T) {
	init := bytes.Repeat([]byte{0x66}, 64)
	segs := [][]byte{
		bytes.Repeat([]byte{0x11}, 4096),
		bytes.Repeat([]byte{0x12}, 4096),
		bytes.Repeat([]byte{0x13}, 100),
	}
	srv := newDASHServer(t, init, segs, "number")
	tr := New(nil)
	w := newTestWriter(totalBytes(init, segs), 3000)
	bg := &countingBudget{}
	if err := tr.Run(context.Background(), streamResource(srv.URL+"/manifest.mpd"), w, bg); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := w.concatenation(); !bytes.Equal(got, allBytes(init, segs)) {
		t.Fatalf("concatenated %d bytes, want exact %d", len(got), totalBytes(init, segs))
	}
	if bg.charged() != int(totalBytes(init, segs)) {
		t.Fatalf("budget charged %d, want %d", bg.charged(), totalBytes(init, segs))
	}
}

func TestRunResumesFromVerifiedProgress(t *testing.T) {
	init := bytes.Repeat([]byte{0x66}, 100)
	segs := [][]byte{bytes.Repeat([]byte{0x21}, 5000), bytes.Repeat([]byte{0x22}, 5000)}
	srv := newDASHServer(t, init, segs, "number")
	tr := New(nil)
	w := newTestWriter(totalBytes(init, segs), 3000)
	// Crash recovery: chunk 0 (init + part of segment 0) is verified on disk.
	w.markDoneUpTo(1)
	if err := tr.Run(context.Background(), streamResource(srv.URL+"/manifest.mpd"), w, nil); err != nil {
		t.Fatalf("Run resumed: %v", err)
	}
	got := w.concatenation()
	want := allBytes(init, segs)[3000:]
	if !bytes.Equal(got, want) {
		t.Fatalf("resumed tail mismatch: got %d bytes, want %d", len(got), len(want))
	}
}

func TestRunResumesInsideLaterSegment(t *testing.T) {
	// Resume point inside segment 1 (offset 6000 in init+seg0+seg1): the
	// source starts at segment 1 and the skip is measured from its offset.
	init := bytes.Repeat([]byte{0x66}, 100)
	segs := [][]byte{bytes.Repeat([]byte{0x41}, 5000), bytes.Repeat([]byte{0x42}, 5000)}
	srv := newDASHServer(t, init, segs, "number")
	tr := New(nil)
	w := newTestWriter(totalBytes(init, segs), 3000)
	w.markDoneUpTo(2) // chunks [0,6000) verified
	if err := tr.Run(context.Background(), streamResource(srv.URL+"/manifest.mpd"), w, nil); err != nil {
		t.Fatalf("Run cross-segment resume: %v", err)
	}
	got := w.concatenation()
	want := allBytes(init, segs)[6000:]
	if !bytes.Equal(got, want) {
		t.Fatalf("cross-segment resume mismatch: got %d bytes, want %d", len(got), len(want))
	}
}

func TestRunRejectsDynamicMPD(t *testing.T) {
	srv := newDASHServer(t, []byte{1}, [][]byte{[]byte{2}}, "number")
	tr := New(nil)
	w := newTestWriter(100, 100)
	err := tr.Run(context.Background(), streamResource(srv.URL+"/dynamic.mpd"), w, nil)
	if err == nil || !strings.Contains(err.Error(), "dynamic") {
		t.Fatalf("dynamic err = %v, want a dynamic rejection", err)
	}
}

func TestRunRejectsDRM(t *testing.T) {
	srv := newDASHServer(t, []byte{1}, [][]byte{[]byte{2}}, "number")
	tr := New(nil)
	w := newTestWriter(100, 100)
	err := tr.Run(context.Background(), streamResource(srv.URL+"/drm.mpd"), w, nil)
	if err == nil || !strings.Contains(err.Error(), "ContentProtection") {
		t.Fatalf("DRM err = %v, want a ContentProtection rejection", err)
	}
}

func TestRunRejectsMultiAdaptationSet(t *testing.T) {
	srv := newDASHServer(t, []byte{1}, [][]byte{[]byte{2}}, "number")
	tr := New(nil)
	w := newTestWriter(100, 100)
	err := tr.Run(context.Background(), streamResource(srv.URL+"/multi.mpd"), w, nil)
	if err == nil || !strings.Contains(err.Error(), "Remux") {
		t.Fatalf("multi-set err = %v, want the WBS 14 Remux pointer", err)
	}
}

func TestRunRejectsSegmentFailure(t *testing.T) {
	init := bytes.Repeat([]byte{0x66}, 50)
	segs := [][]byte{bytes.Repeat([]byte{0x31}, 100), bytes.Repeat([]byte{0x32}, 100)}
	srv := newDASHServer(t, init, segs, "number")
	srv.mu.Lock()
	srv.failSegment = 1
	srv.mu.Unlock()
	tr := New(nil)
	w := newTestWriter(totalBytes(init, segs), 100)
	err := tr.Run(context.Background(), streamResource(srv.URL+"/manifest.mpd"), w, nil)
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("segment failure err = %v, want HTTP 404", err)
	}
}

func TestProbeRejectsBlockedSegmentHost(t *testing.T) {
	// The MPD points its segments at an RFC1918 host via BaseURL: the SSRF
	// guard must refuse before any byte moves.
	mux := http.NewServeMux()
	mux.HandleFunc("/evil.mpd", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<?xml version="1.0"?><MPD type="static" mediaPresentationDuration="PT1S">
  <Period><BaseURL>http://10.99.99.99/</BaseURL>
  <AdaptationSet><Representation id="v" bandwidth="1"><SegmentTemplate timescale="1" duration="1" media="s$Number$.m4s"/></Representation></AdaptationSet></Period></MPD>`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	tr := New(testGuard(t))
	_, err := tr.Probe(context.Background(), streamResource(srv.URL+"/evil.mpd"))
	if err == nil {
		t.Fatal("private segment host must be blocked by the SSRF guard")
	}
}

func TestSupportsOnlyStreamKind(t *testing.T) {
	tr := New(nil)
	if tr.Supports(&core.Resource{Kind: core.KindSingle}) {
		t.Fatal("single resources are not DASH")
	}
	if tr.Supports(nil) {
		t.Fatal("nil resource must not be supported")
	}
	// The two stream transports partition the kind (ADR-0024): DASH claims
	// its marker or a .mpd URL; a bare stream resource belongs to HLS.
	if !tr.Supports(&core.Resource{Kind: core.KindStream,
		Headers: map[string]string{core.HeaderStreamKind: core.StreamKindDASH}}) {
		t.Fatal("dash-marked stream resources must be supported")
	}
	if !tr.Supports(&core.Resource{Kind: core.KindStream,
		Mirrors: []core.Mirror{{URL: "https://a.example.com/live.mpd"}}}) {
		t.Fatal(".mpd stream resources must be supported")
	}
	if tr.Supports(&core.Resource{Kind: core.KindStream,
		Headers: map[string]string{core.HeaderStreamKind: core.StreamKindHLS}}) {
		t.Fatal("hls-marked stream resources belong to the hls transport")
	}
	if tr.Supports(&core.Resource{Kind: core.KindStream,
		Mirrors: []core.Mirror{{URL: "https://a.example.com/live.m3u8"}}}) {
		t.Fatal(".m3u8 stream resources belong to the hls transport")
	}
	if tr.Supports(&core.Resource{Kind: core.KindTorrent,
		Mirrors: []core.Mirror{{URL: "https://a.example.com/live.mpd"}}}) {
		t.Fatal("torrent kind is not DASH")
	}
}

func TestRunWithoutMirrorsFails(t *testing.T) {
	tr := New(nil)
	r := &core.Resource{Kind: core.KindStream}
	if _, err := tr.Probe(context.Background(), r); err == nil {
		t.Fatal("Probe without mirrors must fail")
	}
	if err := tr.Run(context.Background(), r, newTestWriter(1, 1), nil); err == nil {
		t.Fatal("Run without mirrors must fail")
	}
}

func TestRunRejectsBadKind(t *testing.T) {
	tr := New(nil)
	srv := newDASHServer(t, []byte{1}, [][]byte{[]byte{2}}, "number")
	r := streamResource(srv.URL + "/manifest.mpd")
	r.Kind = core.KindSingle
	if _, err := tr.Probe(context.Background(), r); err == nil {
		t.Fatal("Probe must reject non-stream kinds")
	}
	if err := tr.Run(context.Background(), r, newTestWriter(1, 1), nil); err == nil {
		t.Fatal("Run must reject non-stream kinds")
	}
}

func TestDefaultName(t *testing.T) {
	if got := defaultName("https://a.example.com/live/manifest.mpd?tok=1"); got != "manifest.mp4" {
		t.Fatalf("defaultName = %q", got)
	}
	if got := defaultName("https://a.example.com/"); got != "stream.mp4" {
		t.Fatalf("defaultName = %q", got)
	}
}
