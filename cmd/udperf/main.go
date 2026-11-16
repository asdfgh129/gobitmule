// Command udperf measures the project's REAL HTTP Range Transport on a 1 GiB
// deterministic fixture. It supports two modes:
//
//   - default: loopback reference on one machine. It makes the §12 workload
//     reproducible anywhere, but it is NOT the NFR-PERF-01 gate: loopback has
//     no NIC / switch / wire bottleneck, so it always reports
//     nfr_perf_01.met = false.
//   - --serve / --url: a real two-host lab run. One host serves the fixture
//     over TCP (--serve); the other downloads it with internal/transport/http
//     and verifies every chunk (--url). Only this mode can set
//     nfr_perf_01.met = true, and only against an explicit --min-mb-s
//     threshold (ADR-0022: >= 80% of the usable link, i.e. >= 10 MB/s on the
//     current 100 Mbps segment).
//
// CI does not run this target; the two-host gate is a manual lab step.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/asdfgh129/ud/internal/config"
	"github.com/asdfgh129/ud/internal/core"
	"github.com/asdfgh129/ud/internal/security"
	httptransport "github.com/asdfgh129/ud/internal/transport/http"
)

const (
	defaultSize = int64(1) << 30 // 1 GiB, the NFR-PERF-01 workload size
	pieceSize   = int64(256) << 10
	patternSize = 64 << 10
)

// patternFile is a deterministic 1 GiB io.ReadSeeker. Generating a real 1 GiB
// in-memory fixture would double the benchmark's memory footprint and measure
// allocation rather than the transport, so bytes are produced from a 64 KiB
// repeating pattern on demand.
type patternFile struct {
	size    int64
	pos     int64
	pattern []byte
}

func newPatternFile(size int64, pattern []byte) *patternFile {
	return &patternFile{size: size, pattern: pattern}
}

func (p *patternFile) Read(b []byte) (int, error) {
	if p.pos >= p.size {
		return 0, io.EOF
	}
	limit := len(b)
	if int64(limit) > p.size-p.pos {
		limit = int(p.size - p.pos)
	}
	written := 0
	for written < limit {
		off := p.pos % int64(len(p.pattern))
		n := copy(b[written:limit], p.pattern[off:])
		if n == 0 {
			break
		}
		written += n
		p.pos += int64(n)
	}
	return written, nil
}

func (p *patternFile) Seek(offset int64, whence int) (int64, error) {
	var base int64
	switch whence {
	case io.SeekStart:
		base = 0
	case io.SeekCurrent:
		base = p.pos
	case io.SeekEnd:
		base = p.size
	default:
		return 0, fmt.Errorf("invalid whence %d", whence)
	}
	next := base + offset
	if next < 0 {
		return 0, fmt.Errorf("negative seek position")
	}
	p.pos = next
	return next, nil
}

// discardWriter hands out the whole chunk plan sequentially and validates the
// byte count of every chunk. It measures the transport, not disk writes.
type discardWriter struct {
	mu       sync.Mutex
	size     int64
	piece    int64
	next     int64
	received int64
	chunks   int64
}

func (w *discardWriter) Next(context.Context) (core.Chunk, bool, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.next >= w.size {
		return core.Chunk{}, false, nil
	}
	length := w.piece
	if w.next+length > w.size {
		length = w.size - w.next
	}
	c := core.Chunk{Index: int(w.next / w.piece), Start: w.next, Length: length, State: core.ChunkAssigned}
	w.next += length
	return c, true, nil
}

func (w *discardWriter) WriteChunk(_ context.Context, c core.Chunk, p []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if int64(len(p)) != c.Length {
		return fmt.Errorf("chunk %d: received %d bytes, want %d", c.Index, len(p), c.Length)
	}
	w.received += int64(len(p))
	w.chunks++
	return nil
}

func (w *discardWriter) Verified(context.Context, core.Chunk, core.Hash) error { return nil }
func (w *discardWriter) Release(context.Context, core.Chunk, error) error      { return nil }

type nfrVerdict struct {
	Target string `json:"target"`
	Met    bool   `json:"met"`
	Reason string `json:"reason"`
}

type perfReport struct {
	Schema         string         `json:"schema"`
	GeneratedAt    string         `json:"generated_at"`
	Scope          string         `json:"scope"`
	Workload       map[string]any `json:"workload"`
	ElapsedMS      int64          `json:"elapsed_ms"`
	MBPerS         float64        `json:"MB_per_s"`
	BytesVerified  int64          `json:"bytes_verified"`
	ChunksVerified int64          `json:"chunks_verified"`
	NFRPerf01      nfrVerdict     `json:"nfr_perf_01"`
	Reproduce      string         `json:"reproduce"`
}

func main() {
	out := flag.String("out", "dist/perf.json", "machine-readable evidence path")
	sizeFlag := flag.Int64("bytes", defaultSize, "workload size in bytes (default 1 GiB)")
	conns := flag.Int("connections", 4, "MaxConnections for the task")
	serve := flag.Bool("serve", false, "serve the 1 GiB fixture for a two-host run instead of downloading")
	listen := flag.String("listen", "127.0.0.1:0", "listen address for --serve (use the lab IP:port)")
	advertise := flag.String("advertise", "", "address to print in the --serve URL (defaults to --listen)")
	remote := flag.String("url", "", "download this URL with the real Range transport (two-host lab mode)")
	minMBs := flag.Float64("min-mb-s", 0, "two-host mode: required MB/s for nfr_perf_01.met (ADR-0022)")
	requireMet := flag.Bool("require-met", false, "exit non-zero unless nfr_perf_01.met is true")
	flag.Parse()
	size := *sizeFlag
	if size <= 0 || *conns <= 0 {
		fmt.Fprintln(os.Stderr, "udperf: --bytes and --connections must be positive")
		os.Exit(2)
	}
	pattern := makePattern()

	if *serve {
		if err := runServe(*listen, *advertise, size, pattern); err != nil {
			fmt.Fprintln(os.Stderr, "udperf: serve:", err)
			os.Exit(1)
		}
		return
	}

	var srvURL, scope, serverDesc string
	twoHost := *remote != ""
	if twoHost {
		srvURL = *remote
		scope = "two-host lab: 1 GiB over a real TCP link via the real HTTP Range Transport (NFR-PERF-01 workload)"
		serverDesc = "remote udperf --serve, deterministic 64 KiB pattern"
	} else {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.ServeContent(w, r, "perf.bin", time.Time{}, newPatternFile(size, pattern))
		}))
		defer srv.Close()
		srvURL = srv.URL + "/perf.bin"
		scope = "1 GiB loopback reference via the real HTTP Range Transport (NOT NFR-PERF-01)"
		serverDesc = "httptest loopback, deterministic 64 KiB pattern"
	}

	guard, err := clientGuard(srvURL, twoHost)
	if err != nil {
		fmt.Fprintln(os.Stderr, "udperf: guard:", err)
		os.Exit(1)
	}
	tr := httptransport.New(guard)
	res := &core.Resource{
		ID: "perf", Kind: core.KindSingle,
		Files:   []core.ResourceFile{{Index: 0, Path: "perf.bin", Size: size, Selected: true}},
		Mirrors: []core.Mirror{{URL: srvURL, Weight: 1}},
		Limits:  core.TaskLimits{MaxConnections: *conns},
	}
	w := &discardWriter{size: size, piece: pieceSize}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	start := time.Now()
	runErr := tr.Run(ctx, res, w, nil)
	elapsed := time.Since(start)
	if runErr != nil {
		fmt.Fprintf(os.Stderr, "udperf: transport failed after %s: %v\n", elapsed, runErr)
		os.Exit(1)
	}
	if w.received != size {
		fmt.Fprintf(os.Stderr, "udperf: received %d bytes, want %d\n", w.received, size)
		os.Exit(1)
	}
	mbps := float64(size) / (1024 * 1024) / elapsed.Seconds()
	verdict := nfrVerdictFor(twoHost, mbps, *minMBs, w.received == size)
	rep := perfReport{
		Schema:      "ud.perf/v1",
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Scope:       scope,
		Workload: map[string]any{
			"bytes": size, "piece_bytes": pieceSize, "connections": *conns,
			"transport": "internal/transport/http Range path", "server": serverDesc,
		},
		ElapsedMS:      elapsed.Milliseconds(),
		MBPerS:         mbps,
		BytesVerified:  w.received,
		ChunksVerified: w.chunks,
		NFRPerf01:      verdict,
		Reproduce:      reproduceCommand(twoHost, srvURL, size, *conns, *minMBs),
	}
	blob, _ := json.MarshalIndent(rep, "", "  ")
	_ = os.MkdirAll(filepath.Dir(*out), 0o755)
	if err := os.WriteFile(*out, blob, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "udperf: write evidence:", err)
		os.Exit(1)
	}
	kind := "loopback reference"
	if twoHost {
		kind = "two-host lab"
	}
	fmt.Printf("udperf: %d bytes in %s = %.2f MB/s (%s); NFR-PERF-01 met=%v\n",
		size, elapsed.Round(time.Millisecond), mbps, kind, verdict.Met)
	fmt.Println(string(blob))
	if *requireMet && !verdict.Met {
		os.Exit(1)
	}
}

func makePattern() []byte {
	pattern := make([]byte, patternSize)
	for i := range pattern {
		pattern[i] = byte((i*31 + 7) % 256)
	}
	return pattern
}

// runServe exposes the exact 1 GiB NFR workload over TCP so a second host can
// download it with the real transport. It prints one JSON line with the URL
// before serving, then blocks until SIGINT/SIGTERM.
func runServe(listen, advertise string, size int64, pattern []byte) error {
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "perf.bin", time.Time{}, newPatternFile(size, pattern))
	})}

	base := advertise
	if base == "" {
		base = ln.Addr().String()
	}
	host, port, err := net.SplitHostPort(base)
	if err != nil {
		_, lnPort, splitErr := net.SplitHostPort(ln.Addr().String())
		if splitErr != nil {
			return splitErr
		}
		host, port = base, lnPort
	}
	serveURL := "http://" + net.JoinHostPort(host, port) + "/perf.bin"
	blob, _ := json.Marshal(map[string]any{
		"schema": "ud.perf-serve/v1", "url": serveURL, "bytes": size,
		"listen": ln.Addr().String(),
	})
	fmt.Println(string(blob))

	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	select {
	case <-sig:
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(ctx)
	case serveErr := <-errCh:
		if serveErr == http.ErrServerClosed {
			return nil
		}
		return serveErr
	}
}

// clientGuard builds the URL guard for the run. Loopback keeps the strict dev
// guard. Two-host lab mode must reach the peer on the isolated segment, so it
// allows private addresses but pins the exact scheme and port, with no
// redirects. This permissive guard exists only in this lab command.
func clientGuard(raw string, twoHost bool) (core.URLGuard, error) {
	if !twoHost {
		return security.NewDevGuard(), nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	port := 80
	if u.Scheme == "https" {
		port = 443
	}
	if p := u.Port(); p != "" {
		n, convErr := strconv.Atoi(p)
		if convErr != nil {
			return nil, fmt.Errorf("invalid URL port %q: %w", p, convErr)
		}
		port = n
	}
	return security.NewSSRFGuard(config.SecurityConfig{
		AllowedSchemes:       []string{"http", "https"},
		AllowedPorts:         []int{port},
		MaxRedirects:         0,
		BlockPrivateNetworks: false,
	}), nil
}

func nfrVerdictFor(twoHost bool, mbps, minMBs float64, verified bool) nfrVerdict {
	if !twoHost {
		return nfrVerdict{
			Target: "1 GiB over a real two-host gigabit LAN at >= 80 MB/s",
			Met:    false,
			Reason: "loopback-only measurement: no second host, NIC, switch or wire bottleneck; a loopback MB/s value cannot satisfy the LAN criterion. Run a two-host gigabit-LAN lab to close this NFR.",
		}
	}
	if minMBs <= 0 {
		return nfrVerdict{
			Target: "1 GiB over a real two-host link (threshold not supplied)",
			Met:    false,
			Reason: "two-host run without --min-mb-s: throughput was measured but no gate threshold was supplied, so it cannot be marked met",
		}
	}
	met := verified && mbps >= minMBs
	reason := fmt.Sprintf("two-host lab: %.2f MB/s, all bytes verified=%t; threshold >= %.2f MB/s (ADR-0022: >=80%% of usable link; current 100 Mbps segment >=10 MB/s)",
		mbps, verified, minMBs)
	if !met {
		reason += "; NOT met"
	}
	return nfrVerdict{
		Target: fmt.Sprintf("1 GiB over a real two-host link at >= %.2f MB/s (ADR-0022)", minMBs),
		Met:    met,
		Reason: reason,
	}
}

func reproduceCommand(twoHost bool, srvURL string, size int64, conns int, minMBs float64) string {
	if !twoHost {
		return "make perf (or go run ./cmd/udperf --bytes 1073741824)"
	}
	return fmt.Sprintf("host B: go run ./cmd/udperf --serve --listen <lab-ip>:18080 --bytes %d ; host A: go run ./cmd/udperf --url %s --bytes %d --connections %d --min-mb-s %.1f --require-met --out dist/perf_lab.json",
		size, srvURL, size, conns, minMBs)
}
