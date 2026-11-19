package hlstransport

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/asdfgh129/ud/internal/config"
	"github.com/asdfgh129/ud/internal/core"
	"github.com/asdfgh129/ud/internal/security"
)

// testWriter is the scheduler-owned ChunkWriter stand-in (same shape as the
// ftp test double): a fixed chunk plan over one file.
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

// markDoneUpTo simulates crash-recovered verified progress.
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

// countingBudget charges every byte through the scheduler's Budget contract.
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

// testGuard allows loopback (the httptest fixture) while still blocking
// private ranges — the exact shape the sftp integration tests use.
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

// hlsServer serves deterministic VOD playlists. Segments are opaque bytes:
// the L1 transport concatenates them without inspecting the payload, so the
// fixture does not need real MPEG-TS.
type hlsServer struct {
	*httptest.Server
	mu          sync.Mutex
	segments    [][]byte
	failSegment int
	// encrypted, when true, serves /media.m3u8 with an AES-128 EXT-X-KEY and
	// stores the PLAINTEXT expectation in plain for assertions.
	encrypted bool
	key       []byte
}

func newHLSServer(t *testing.T, segments [][]byte) *hlsServer {
	t.Helper()
	s := &hlsServer{segments: segments, failSegment: -1, key: bytes.Repeat([]byte{0x42}, 16)}
	mux := http.NewServeMux()
	var srv *httptest.Server // filled after NewServer; handlers read it lazily

	mux.HandleFunc("/media.m3u8", func(w http.ResponseWriter, _ *http.Request) {
		var b strings.Builder
		b.WriteString("#EXTM3U\n#EXT-X-TARGETDURATION:10\n#EXT-X-MEDIA-SEQUENCE:0\n")
		if s.encrypted {
			fmt.Fprintf(&b, "#EXT-X-KEY:METHOD=AES-128,URI=\"%s/key.bin\"\n", srv.URL)
		}
		for i := range s.segments {
			fmt.Fprintf(&b, "#EXTINF:10,\nsegment-%d.ts\n", i)
		}
		b.WriteString("#EXT-X-ENDLIST\n")
		_, _ = w.Write([]byte(b.String()))
	})
	mux.HandleFunc("/media-noend.m3u8", func(w http.ResponseWriter, _ *http.Request) {
		var b strings.Builder
		b.WriteString("#EXTM3U\n#EXT-X-TARGETDURATION:10\n")
		for i := range s.segments {
			fmt.Fprintf(&b, "#EXTINF:10,\nsegment-%d.ts\n", i)
		}
		_, _ = w.Write([]byte(b.String())) // live: no ENDLIST
	})
	mux.HandleFunc("/evil.m3u8", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-TARGETDURATION:10\n#EXTINF:10,\nhttp://10.99.99.99/seg.ts\n#EXT-X-ENDLIST\n"))
	})
	mux.HandleFunc("/key.bin", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(s.key)
	})
	for i, body := range s.segments {
		seg, idx := body, i
		mux.HandleFunc(fmt.Sprintf("/segment-%d.ts", i), func(w http.ResponseWriter, r *http.Request) {
			s.mu.Lock()
			fail := s.failSegment == idx
			s.mu.Unlock()
			if fail {
				http.Error(w, "synthetic failure", http.StatusNotFound)
				return
			}
			payload := seg
			if s.encrypted {
				var iv [16]byte
				iv[15] = byte(idx) // sequence-derived IV
				payload = encryptLikeCDN(t, s.key, iv[:], seg)
			}
			// Range 0-0 probing must answer 206 with the full (on-wire) total.
			if r.Header.Get("Range") == probeRange {
				w.Header().Set("Content-Range", fmt.Sprintf("bytes 0-0/%d", len(payload)))
				w.WriteHeader(http.StatusPartialContent)
				_, _ = w.Write(payload[:1])
				return
			}
			_, _ = w.Write(payload)
		})
	}
	s.Server = httptest.NewServer(mux)
	srv = s.Server // handlers only fire after Serve, so this is safe
	t.Cleanup(s.Close)
	return s
}

func streamResource(url string) *core.Resource {
	return &core.Resource{
		ID:      "res-hls",
		Kind:    core.KindStream,
		Engine:  core.EngineGo,
		Files:   []core.ResourceFile{{Index: 0, Path: "media.ts", Selected: true}},
		Mirrors: []core.Mirror{{URL: url}},
	}
}

func totalSize(segs [][]byte) int64 {
	var n int64
	for _, s := range segs {
		n += int64(len(s))
	}
	return n
}

func TestProbeSumsSegmentSizes(t *testing.T) {
	segs := [][]byte{bytes.Repeat([]byte("A"), 1000), bytes.Repeat([]byte("B"), 2500), bytes.Repeat([]byte("C"), 17)}
	srv := newHLSServer(t, segs)
	tr := New(nil)
	p, err := tr.Probe(context.Background(), streamResource(srv.URL+"/media.m3u8"))
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if p.Size != totalSize(segs) {
		t.Fatalf("size = %d, want %d", p.Size, totalSize(segs))
	}
	if !p.AcceptRanges || p.Filename != "media.ts" {
		t.Fatalf("probe = %+v", p)
	}
}

func TestRunConcatenatesSegmentsAndChargesBudget(t *testing.T) {
	segs := [][]byte{
		bytes.Repeat([]byte{0x10}, 4096),
		bytes.Repeat([]byte{0x11}, 4096),
		bytes.Repeat([]byte{0x12}, 100),
	}
	srv := newHLSServer(t, segs)
	tr := New(nil)
	w := newTestWriter(totalSize(segs), 3000)
	bg := &countingBudget{}
	if err := tr.Run(context.Background(), streamResource(srv.URL+"/media.m3u8"), w, bg); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := w.concatenation(); !bytes.Equal(got, bytes.Join(segs, nil)) {
		t.Fatalf("concatenated %d bytes, want exact %d", len(got), totalSize(segs))
	}
	if bg.charged() != int(totalSize(segs)) {
		t.Fatalf("budget charged %d, want %d", bg.charged(), totalSize(segs))
	}
}

func TestRunResumesFromVerifiedProgress(t *testing.T) {
	segs := [][]byte{bytes.Repeat([]byte{0x20}, 5000), bytes.Repeat([]byte{0x21}, 5000)}
	srv := newHLSServer(t, segs)
	tr := New(nil)
	w := newTestWriter(totalSize(segs), 3000)
	// Crash recovery: chunk 0 is verified on disk; the stream must skip its
	// bytes and fill the remaining chunks exactly.
	w.markDoneUpTo(1)
	if err := tr.Run(context.Background(), streamResource(srv.URL+"/media.m3u8"), w, nil); err != nil {
		t.Fatalf("Run with resumed progress: %v", err)
	}
	// The writer only holds chunks 1..3: their concatenation must equal the
	// stream tail starting at chunk 0's end (3000).
	got := w.concatenation()
	want := bytes.Join(segs, nil)[3000:]
	if !bytes.Equal(got, want) {
		t.Fatalf("resumed tail mismatch: got %d bytes, want %d", len(got), len(want))
	}
}

func TestRunEncryptedSegmentsSequenceIV(t *testing.T) {
	segs := [][]byte{bytes.Repeat([]byte{0x33}, 2400), bytes.Repeat([]byte{0x34}, 1600)}
	srv := newHLSServer(t, segs)
	srv.encrypted = true
	tr := New(nil)
	w := newTestWriter(totalSize(segs), 1000)
	if err := tr.Run(context.Background(), streamResource(srv.URL+"/media.m3u8"), w, nil); err != nil {
		t.Fatalf("Run encrypted: %v", err)
	}
	if got := w.concatenation(); !bytes.Equal(got, bytes.Join(segs, nil)) {
		t.Fatalf("decrypted concatenation mismatch: got %d bytes, want %d", len(got), totalSize(segs))
	}
}

func TestRunRejectsLivePlaylist(t *testing.T) {
	segs := [][]byte{bytes.Repeat([]byte{0x40}, 100)}
	srv := newHLSServer(t, segs)
	tr := New(nil)
	w := newTestWriter(100, 100)
	err := tr.Run(context.Background(), streamResource(srv.URL+"/media-noend.m3u8"), w, nil)
	if err == nil || !strings.Contains(err.Error(), "live") {
		t.Fatalf("live playlist err = %v, want a live rejection", err)
	}
}

func TestRunRejectsSegmentFailure(t *testing.T) {
	segs := [][]byte{bytes.Repeat([]byte{0x50}, 100), bytes.Repeat([]byte{0x51}, 100)}
	srv := newHLSServer(t, segs)
	srv.mu.Lock()
	srv.failSegment = 1
	srv.mu.Unlock()
	tr := New(nil)
	w := newTestWriter(totalSize(segs), 100)
	err := tr.Run(context.Background(), streamResource(srv.URL+"/media.m3u8"), w, nil)
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("segment failure err = %v, want HTTP 404", err)
	}
}

func TestProbeRejectsBlockedSegmentHost(t *testing.T) {
	segs := [][]byte{bytes.Repeat([]byte{0x60}, 50)}
	srv := newHLSServer(t, segs)
	// /evil.m3u8 points its single segment at an RFC1918 host: the SSRF guard
	// must refuse before any byte moves.
	tr := New(testGuard(t))
	_, err := tr.Probe(context.Background(), streamResource(srv.URL+"/evil.m3u8"))
	if err == nil {
		t.Fatal("private segment host must be blocked by the SSRF guard")
	}
}

func TestRunMasterSelectsBestVariant(t *testing.T) {
	lo := bytes.Repeat([]byte{0x70}, 200)
	hi := bytes.Repeat([]byte{0x71}, 400)
	mux := http.NewServeMux()
	mux.HandleFunc("/master.m3u8", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("#EXTM3U\n" +
			"#EXT-X-STREAM-INF:BANDWIDTH=100000\nlo.m3u8\n" +
			"#EXT-X-STREAM-INF:BANDWIDTH=900000\nhi.m3u8\n"))
	})
	mux.HandleFunc("/lo.m3u8", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-TARGETDURATION:10\n#EXTINF:10,\nseg-lo.ts\n#EXT-X-ENDLIST\n"))
	})
	mux.HandleFunc("/hi.m3u8", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-TARGETDURATION:10\n#EXTINF:10,\nseg-hi.ts\n#EXT-X-ENDLIST\n"))
	})
	mux.HandleFunc("/seg-lo.ts", func(w http.ResponseWriter, r *http.Request) { serveSegmentBytes(w, r, lo) })
	mux.HandleFunc("/seg-hi.ts", func(w http.ResponseWriter, r *http.Request) { serveSegmentBytes(w, r, hi) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	tr := New(nil)
	p, err := tr.Probe(context.Background(), streamResource(srv.URL+"/master.m3u8"))
	if err != nil {
		t.Fatalf("Probe master: %v", err)
	}
	if p.Size != int64(len(hi)) {
		t.Fatalf("master selected the wrong variant: size %d, want %d", p.Size, len(hi))
	}
	w := newTestWriter(int64(len(hi)), 100)
	if err := tr.Run(context.Background(), streamResource(srv.URL+"/master.m3u8"), w, nil); err != nil {
		t.Fatalf("Run master: %v", err)
	}
	if !bytes.Equal(w.concatenation(), hi) {
		t.Fatal("downloaded bytes are not from the best variant")
	}
}

func serveSegmentBytes(w http.ResponseWriter, r *http.Request, body []byte) {
	if r.Header.Get("Range") == probeRange {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes 0-0/%d", len(body)))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(body[:1])
		return
	}
	_, _ = w.Write(body)
}

func TestSupportsOnlyStreamKind(t *testing.T) {
	tr := New(nil)
	if tr.Supports(&core.Resource{Kind: core.KindSingle}) {
		t.Fatal("single resources are not HLS")
	}
	if tr.Supports(nil) {
		t.Fatal("nil resource must not be supported")
	}
	if !tr.Supports(&core.Resource{Kind: core.KindStream}) {
		t.Fatal("stream resources must be supported")
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
	srv := newHLSServer(t, [][]byte{[]byte("x")})
	r := streamResource(srv.URL + "/media.m3u8")
	r.Kind = core.KindSingle
	if _, err := tr.Probe(context.Background(), r); err == nil {
		t.Fatal("Probe must reject non-stream kinds")
	}
	if err := tr.Run(context.Background(), r, newTestWriter(1, 1), nil); err == nil {
		t.Fatal("Run must reject non-stream kinds")
	}
}

func TestKeyFetchShapes(t *testing.T) {
	raw := bytes.Repeat([]byte{0x11}, 16)
	mux := http.NewServeMux()
	mux.HandleFunc("/raw", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(raw) })
	mux.HandleFunc("/hex", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("11", 16)))
	})
	mux.HandleFunc("/badshape", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("short")) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	tr := New(nil)
	k1, err := tr.fetchKey(context.Background(), &Key{Method: "AES-128", URI: srv.URL + "/raw"}, nil)
	if err != nil || !bytes.Equal(k1, raw) {
		t.Fatalf("raw key fetch: %v", err)
	}
	k2, err := tr.fetchKey(context.Background(), &Key{Method: "AES-128", URI: srv.URL + "/hex"}, nil)
	if err != nil || !bytes.Equal(k2, raw) {
		t.Fatalf("hex key fetch: %v", err)
	}
	// The cache must serve the second request for the same URI.
	k3, err := tr.fetchKey(context.Background(), &Key{Method: "AES-128", URI: srv.URL + "/raw"}, nil)
	if err != nil || !bytes.Equal(k3, raw) {
		t.Fatalf("cached key: %v", err)
	}
	if _, err := tr.fetchKey(context.Background(), &Key{Method: "AES-128", URI: srv.URL + "/badshape"}, nil); err == nil {
		t.Fatal("wrong key size must fail")
	}
}
