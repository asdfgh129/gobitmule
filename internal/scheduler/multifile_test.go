package scheduler

// ADR-0027 integration: a separated audio/video task downloads both streams
// (one file each, own chunk plan and .part) and the built-in remux merges
// them into ONE playable MP4 — no ffmpeg (docs/06 §4.4, ADR-0010 验证清单
// 「删除 ffmpeg 后仍能拿到高画质格式并完成合并」的调度器级实现).

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asdfgh129/gobitmule/internal/config"
	"github.com/asdfgh129/gobitmule/internal/core"
	"github.com/asdfgh129/gobitmule/internal/postproc"
	"github.com/asdfgh129/gobitmule/internal/resolve"
	"github.com/asdfgh129/gobitmule/internal/security"
	"github.com/asdfgh129/gobitmule/internal/store"
	"github.com/asdfgh129/gobitmule/internal/transport"
	httptransport "github.com/asdfgh129/gobitmule/internal/transport/http"
)

// --- synthetic MPEG-TS fixtures (duplicated from postproc tests: helpers are
// test-local there and the separated streams need per-track variants) ---

func mfTsPkt(pid uint16, pusi bool, payload []byte) []byte {
	p := make([]byte, 188)
	p[0] = 0x47
	p[1] = byte(pid>>8) & 0x1f
	p[2] = byte(pid)
	p[3] = 0x10
	if pusi {
		p[1] |= 0x40
	}
	copy(p[4:], payload)
	return p
}

func mfTsPAT() []byte {
	sec := []byte{0, 0x00, 0xb0, 0x0d, 0, 1, 0xc1, 0, 0, 0, 1, 0xf0, 0x00, 0, 0, 0, 0}
	return mfTsPkt(0, true, sec)
}

func mfTsPMT() []byte {
	// The two-entry PMT from the postproc fixtures is reused verbatim for
	// BOTH separated files: a video-only file simply carries no PID-257
	// packets and an audio-only file no PID-256 packets; the demuxer maps
	// PIDs from the PMT and the muxer tolerates the missing track.
	sec := []byte{
		0, 0x02, 0xb0, 0x17, 0, 1, 0xc1, 0, 0, 0x01, 0x00, 0x00, 0x00,
		0x1b, 0x01, 0x00, 0x00, 0x00,
		0x0f, 0x01, 0x01, 0x00, 0x00,
		0, 0, 0, 0,
	}
	return mfTsPkt(4096, true, sec)
}

var mfSPS = []byte{0x67, 0x42, 0x00, 0x0a, 0xf4, 0xf3}
var mfPPS = []byte{0x68, 0xce, 0x06, 0xe2}

func mfPes(pid uint16, pts int64, payload []byte, withDTS bool) []byte {
	var hdr []byte
	if withDTS {
		v := uint64(pts) & ((1 << 33) - 1)
		d := uint64(pts-10000) & ((1 << 33) - 1)
		hdr = []byte{0, 0, 1, 0xe0, 0, 0, 0x80, 0xc0, 10,
			0x21 | byte((v>>29)&0x0e), byte(v >> 22), 0x01 | byte((v>>14)&0xfe), byte(v >> 7), 0x01 | byte((v<<1)&0xfe),
			0x11 | byte((d>>29)&0x0e), byte(d >> 22), 0x01 | byte((d>>14)&0xfe), byte(d >> 7), 0x01 | byte((d<<1)&0xfe)}
	} else {
		v := uint64(pts) & ((1 << 33) - 1)
		hdr = []byte{0, 0, 1, 0xe0, 0, 0, 0x80, 0x80, 5,
			0x21 | byte((v>>29)&0x0e), byte(v >> 22), 0x01 | byte((v>>14)&0xfe),
			byte(v >> 7), 0x01 | byte((v<<1)&0xfe)}
	}
	if pid == 257 {
		// audio stream uses stream_id 0xc0
		hdr[3] = 0xc0
	}
	plen := len(hdr) - 6 + len(payload)
	hdr[4], hdr[5] = byte(plen>>8), byte(plen)
	out := append(hdr, payload...)
	var buf bytes.Buffer
	buf.Write(mfTsPkt(pid, true, out[:min(184, len(out))]))
	for off := 184; off < len(out); off += 184 {
		buf.Write(mfTsPkt(pid, false, out[min(off+184, len(out)):]))
	}
	return buf.Bytes()
}

func mfAdts(payload []byte) []byte {
	n := len(payload) + 7
	out := []byte{0xff, 0xf1, 0x4c, 0x40, byte(n >> 3), byte((n & 7) << 5), 0xff}
	return append(out, payload...)
}

func mfVideoTS() []byte {
	var b bytes.Buffer
	b.Write(mfTsPAT())
	b.Write(mfTsPMT())
	avc := append([]byte{0, 0, 0, 1}, mfSPS...)
	avc = append(avc, 0, 0, 0, 1)
	avc = append(avc, mfPPS...)
	avc = append(avc, 0, 0, 0, 1, 0x65, 0x88, 0x84, 0x21, 0xa0)
	b.Write(mfPes(256, 100000, avc, true))
	avc2 := []byte{0, 0, 0, 1, 0x41, 0x9a, 0x20, 0x80}
	b.Write(mfPes(256, 180000, avc2, false))
	return b.Bytes()
}

func mfAudioTS() []byte {
	var b bytes.Buffer
	b.Write(mfTsPAT())
	b.Write(mfTsPMT())
	a1 := append(mfAdts([]byte{0xde, 0x02, 0x00, 0x4c}), mfAdts([]byte{0xde, 0x03, 0x00, 0x4d})...)
	b.Write(mfPes(257, 100000, a1, false))
	a2 := mfAdts([]byte{0xde, 0x04, 0x00, 0x4e})
	b.Write(mfPes(257, 180000, a2, false))
	return b.Bytes()
}

// multiFileResolver is the test resolver for "multi://..." inputs: it emits
// the docs/06 §4.4 separated-stream shape (two selected files, per-file URLs
// and declared sizes) pointing at the local fixture server.
type multiFileResolver struct{ videoURL, audioURL string }

func (m *multiFileResolver) Name() string { return "multi-file-test" }
func (m *multiFileResolver) Match(in core.Input) bool {
	return in.Kind == core.InputURL && strings.HasPrefix(in.Raw, "multi://")
}
func (m *multiFileResolver) Resolve(_ core.ResolveContext, in core.Input) (*core.Resource, error) {
	return &core.Resource{
		ID:     "res-multi",
		Kind:   core.KindSingle,
		Engine: core.EngineGo,
		Files: []core.ResourceFile{
			{Index: 0, Path: "movie.mp4", Size: int64(len(mfVideoTS())), Selected: true, URL: m.videoURL},
			{Index: 1, Path: "movie.audio.m4a", Size: int64(len(mfAudioTS())), Selected: true, URL: m.audioURL},
		},
		Mirrors: []core.Mirror{{URL: m.videoURL, Priority: 1}},
		Identity: core.ProtocolIdentity{
			IsAdaptive: true, VariantID: "1080p+dual-audio",
		},
	}, nil
}

// TestMultiFileSeparatedStreamsRemuxToMP4 is the scheduler-level ADR-0010
// verification: two separated streams download under one task, then the
// built-in remux merges them into one MP4 without ffmpeg.
func TestMultiFileSeparatedStreamsRemuxToMP4(t *testing.T) {
	video, audio := mfVideoTS(), mfAudioTS()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body []byte
		switch r.URL.Path {
		case "/v.ts":
			body = video
		case "/a.ts":
			body = audio
		default:
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Range") != "" {
			// HTTP transport always ranges; serve 206 with the whole body.
			w.Header().Set("Content-Range", "bytes 0-"+itoa(int64(len(body)-1))+"/"+itoa(int64(len(body))))
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(body)
			return
		}
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.DownloadDir = dir
	cfg.Global.ChunkSize = "8KiB"
	cfg.Global.MaxDownloadRate = "0"
	cfg.Security.AllowLoopbackForDev = true
	mgr := config.NewManager(cfg, nil)
	bus := core.NewEventBus(256)
	st, err := store.Open(filepath.Join(dir, "state.db"), bus)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	guard := security.NewDevGuard()
	resolvers := resolve.NewRegistry()
	resolvers.Register(&multiFileResolver{
		videoURL: srv.URL + "/v.ts", audioURL: srv.URL + "/a.ts",
	})
	resolvers.Register(resolve.NewDirectHTTP(guard))
	resolvers.Register(resolve.Unsupported{})
	transports := transport.NewRegistry()
	transports.Register(httptransport.New(guard))
	s, err := New(Options{Store: st, Resolvers: resolvers, Transports: transports,
		PostProcessors: postproc.NewRegistry(), Guard: guard,
		Credentials: security.NewMemoryCredentialStore(), Config: mgr, Bus: bus})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close(); srv.Close() })

	created, err := s.Create(context.Background(), core.Input{
		Raw: "multi://movie", Kind: core.InputURL,
	}, core.TaskOptions{Dir: dir})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	s.enqueue(created)

	deadline := time.Now().Add(30 * time.Second)
	for {
		tk, err := st.GetTask(context.Background(), created)
		if err != nil {
			t.Fatal(err)
		}
		if tk.State == core.StateCompleted {
			break
		}
		if tk.State == core.StateFailed || tk.State == core.StateCanceled {
			t.Fatalf("task ended in %s (error: %s)", tk.State, tk.ErrorMessage)
		}
		if time.Now().After(deadline) {
			t.Fatalf("task did not complete in time; state=%s", tk.State)
		}
		time.Sleep(50 * time.Millisecond)
	}

	final := filepath.Join(dir, "movie.mp4")
	raw, err := os.ReadFile(final)
	if err != nil {
		t.Fatalf("merged output missing: %v", err)
	}
	t.Logf("merged size=%d video=%d audio=%d", len(raw), len(mfVideoTS()), len(mfAudioTS()))
	if vb, err := os.ReadFile(filepath.Join(dir, "movie.mp4.part")); err == nil {
		t.Logf("video part bytes=%d first=%x", len(vb), vb[:min(4, len(vb))])
	} else {
		t.Logf("video part: %v", err)
	}
	if ab, err := os.ReadFile(filepath.Join(dir, "movie.audio.m4a.part")); err == nil {
		nonZero := 0
		for _, c := range ab {
			if c != 0 {
				nonZero++
			}
		}
		t.Logf("audio part bytes=%d nonZero=%d first=%x", len(ab), nonZero, ab[:min(4, len(ab))])
	} else {
		t.Logf("audio part: %v", err)
	}
	// The merged MP4 must carry both tracks' payloads: the video sample bytes
	// and the AAC frames from the two separated streams.
	if !bytes.Contains(raw, []byte{0x9a, 0x20, 0x80}) {
		t.Fatal("merged MP4 lost the video samples")
	}
	if !bytes.Contains(raw, []byte{0xde, 0x02, 0x00, 0x4c}) {
		t.Fatal("merged MP4 lost the audio samples")
	}
	// The per-stream .part files must be gone after the merge.
	if _, err := os.Stat(final + ".part"); !os.IsNotExist(err) {
		t.Fatal("video .part survived the merge commit")
	}
	// And the task reports Completed through the store.
	tk, err := st.GetTask(context.Background(), created)
	if err != nil || tk.State != core.StateCompleted {
		t.Fatalf("task state = %s, %v", tk.State, err)
	}
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
