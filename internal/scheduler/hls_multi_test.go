package scheduler

// ADR-0028 integration: an HLS master playlist carrying a separate AUDIO
// rendition expands (via core.FilePlanner) into a two-file plan; the
// scheduler downloads each media playlist into its own .part and the built-in
// remux merges video + audio into one MP4 — no ffmpeg, ADR-0010 verification.

import (
	"bytes"
	"context"
	"fmt"
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
	hlstransport "github.com/asdfgh129/gobitmule/internal/transport/hls"
	httptransport "github.com/asdfgh129/gobitmule/internal/transport/http"
)

// hlsFixtureServer serves a master playlist with a separate audio group plus
// two media playlists whose segments are halves of the corresponding
// synthetic TS streams.
type hlsFixtureServer struct {
	*httptest.Server
	videoSegs [][]byte
	audioSegs [][]byte
}

func newHLSFixtureServer(t *testing.T) *hlsFixtureServer {
	t.Helper()
	s := &hlsFixtureServer{}
	// Split the synthetic streams at packet boundaries (188-byte multiples).
	vts := mfVideoTS()
	ats := mfAudioTS()
	split := vts[len(vts)/188/2*188]
	s.videoSegs = [][]byte{vts[:split], vts[split:]}
	asplit := ats[len(ats)/188/2*188]
	s.audioSegs = [][]byte{ats[:asplit], ats[asplit:]}

	mux := http.NewServeMux()
	media := func(name string, segs [][]byte) string {
		var b strings.Builder
		b.WriteString("#EXTM3U\n#EXT-X-TARGETDURATION:10\n#EXT-X-MEDIA-SEQUENCE:0\n")
		for i := range segs {
			fmt.Fprintf(&b, "#EXTINF:10,\nseg-%s-%d.ts\n", name, i)
		}
		b.WriteString("#EXT-X-ENDLIST\n")
		return b.String()
	}
	mux.HandleFunc("/master.m3u8", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("#EXTM3U\n" +
			"#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID=\"aud\",NAME=\"EN\",DEFAULT=YES,URI=\"audio.m3u8\"\n" +
			"#EXT-X-STREAM-INF:BANDWIDTH=5000000,AUDIO=\"aud\"\nvideo.m3u8\n"))
	})
	mux.HandleFunc("/video.m3u8", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(media("v", s.videoSegs)))
	})
	mux.HandleFunc("/audio.m3u8", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(media("a", s.audioSegs)))
	})
	serve := func(body []byte) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Range") == "bytes=0-0" {
				w.Header().Set("Content-Range", fmt.Sprintf("bytes 0-0/%d", len(body)))
				w.WriteHeader(http.StatusPartialContent)
				_, _ = w.Write(body[:1])
				return
			}
			_, _ = w.Write(body)
		}
	}
	for i, body := range s.videoSegs {
		b, idx := body, i
		mux.HandleFunc(fmt.Sprintf("/seg-v-%d.ts", i), func(w http.ResponseWriter, r *http.Request) {
			serve(b)(w, r)
			_ = idx
		})
	}
	for i, body := range s.audioSegs {
		b := body
		mux.HandleFunc(fmt.Sprintf("/seg-a-%d.ts", i), func(w http.ResponseWriter, r *http.Request) {
			serve(b)(w, r)
		})
	}
	s.Server = httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s
}

func TestHLSSeparatedAudioRenditionRemuxToMP4(t *testing.T) {
	srv := newHLSFixtureServer(t)

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
	resolvers.Register(resolve.NewHLSResolver(guard))
	resolvers.Register(resolve.NewDirectHTTP(guard))
	resolvers.Register(resolve.Unsupported{})
	transports := transport.NewRegistry()
	transports.Register(hlstransport.New(guard))
	transports.Register(httptransport.New(guard))
	s, err := New(Options{Store: st, Resolvers: resolvers, Transports: transports,
		PostProcessors: postproc.NewRegistry(), Guard: guard,
		Credentials:    security.NewMemoryCredentialStore(), Config: mgr, Bus: bus})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	created, err := s.Create(context.Background(), core.Input{
		Raw: srv.URL + "/master.m3u8", Kind: core.InputURL,
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
			t.Fatalf("task did not complete in time; state=%s input=%s", tk.State, tk.Input)
		}
		time.Sleep(50 * time.Millisecond)
	}

	final := filepath.Join(dir, "master.mp4")
	raw, err := os.ReadFile(final)
	if err != nil {
		t.Fatalf("merged output missing: %v", err)
	}
	// Both separated tracks must have contributed samples to the merged MP4.
	if !bytes.Contains(raw, []byte{0x9a, 0x20, 0x80}) {
		t.Fatal("merged MP4 lost the video samples")
	}
	if !bytes.Contains(raw, []byte{0xde, 0x02, 0x00, 0x4c}) {
		t.Fatal("merged MP4 lost the audio samples")
	}
}
