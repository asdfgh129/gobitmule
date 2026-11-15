package main

// BX-05 / BX-06: the OTHER half of ADR-0021 path A ("ud seeding").
//
// cmd/udinteropx already proves third-party seeder -> ud leecher (BX-01..BX-04).
// These two optional cases prove ud seeder -> real third-party leecher:
//
//   1. a REAL aria2-next / libtorrent engine seeds the payload;
//   2. the project's real bttransport downloads it WITH a SeedingPolicy, so
//      Run() hands the completed torrent to a background seeding session;
//   3. the original seeder is STOPPED, so it can no longer serve any byte;
//   4. a fresh real aria2-next / libtorrent leecher downloads from ud's
//      seeding session and the payload is sha256-verified.
//
// Honesty invariants: every case is ONE-WAY, loopback, self-hosted BEP 3
// tracker, DHT/PEX/LSD off, no packet capture, no isolated gigabit LAN, no
// chihaya / opentracker binary and no qBittorrent / Transmission matrix.
// Therefore BX-05 / BX-06 are SUPPLEMENTARY evidence, never BT-01..BT-14
// PASS results; release_gate_met stays false. An unavailable external engine
// emits SKIPPED / UNSUPPORTED, never a fabricated PASS.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/asdfgh129/ud/internal/core"
	"github.com/asdfgh129/ud/internal/interop"
	bt "github.com/asdfgh129/ud/internal/transport/bt"
)

const seedingLeecherScriptDefault = "tools/interop_libtorrent_leecher.py"

// udSeedingPayload is deliberately different from the BX-01..BX-04 payloads so
// a stale file can never accidentally satisfy a hash check.
const udSeedingPayloadSize = 256 * 1024

func seedingTimeout() time.Duration {
	if v := os.Getenv("UD_INTEROPX_TIMEOUT_S"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return time.Duration(n) * time.Second
		}
	}
	return 60 * time.Second
}

func freeLoopbackPort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port, nil
}

func waitTCP(addr string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
		if err == nil {
			_ = c.Close()
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

// memChunkWriter collects the authoritative chunk plan in memory; unlike the
// file writer used by BX-01..BX-04 it needs no temp directory.
type memChunkWriter struct {
	mu   sync.Mutex
	size int64
	next int64
	data []byte
}

func (w *memChunkWriter) Next(context.Context) (core.Chunk, bool, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.next >= w.size {
		return core.Chunk{}, false, nil
	}
	start := w.next
	l := int64(1 << 18)
	if start+l > w.size {
		l = w.size - start
	}
	w.next += l
	return core.Chunk{Start: start, Length: l}, true, nil
}

func (w *memChunkWriter) WriteChunk(_ context.Context, c core.Chunk, p []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if int64(len(w.data)) < c.Start+int64(len(p)) {
		nd := make([]byte, c.Start+int64(len(p)))
		copy(nd, w.data)
		w.data = nd
	}
	copy(w.data[c.Start:], p)
	return nil
}

func (w *memChunkWriter) Verified(context.Context, core.Chunk, core.Hash) error { return nil }
func (w *memChunkWriter) Release(context.Context, core.Chunk, error) error      { return nil }
func (w *memChunkWriter) Bytes() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]byte(nil), w.data...)
}

// downloadAndSeedWithUD downloads from the real seeder with path A enabled and
// returns the live Transport (its background seeding session must be closed
// by the caller). A nil Transport means no session was handed off.
func downloadAndSeedWithUD(torrentBytes []byte, announce string, size int64, peer net.Addr, listenPort int) (*bt.Transport, []byte, error) {
	w := &memChunkWriter{size: size}
	res := &core.Resource{
		ID:   "ud-seeding-bx",
		Kind: core.KindTorrent,
		Files: []core.ResourceFile{{
			Path: "payload.bin", Selected: true,
		}},
		Identity: core.ProtocolIdentity{
			TorrentMeta: torrentBytes,
			Trackers:    [][]string{{announce}},
			// Private=false: path A seeding applies. DHT/PEX are still
			// disabled by the policy below, so the lab stays tracker-only.
		},
	}
	policy := bt.SeedingPolicy{
		Enabled:        true,
		SeedRatio:      0, // ratio target off: keep serving until Close()
		SeedTime:       10 * time.Minute,
		MaxUploadSlots: 8,
		EnableUPnP:     false,
		EnableDHT:      false,
		EnablePEX:      false,
		PeerID:         "-UD0000-",
		ListenPort:     listenPort,
		ListenHost:     "127.0.0.1",
		StatsInterval:  200 * time.Millisecond,
	}
	tr := bt.New(noopGuard{}, noopCreds{},
		bt.WithBootstrapPeers(peer),
		bt.WithPlainHandshake(),
		bt.WithSeedingPolicy(policy),
		bt.WithLifecycleContext(context.Background()),
	)
	ctx, cancel := context.WithTimeout(context.Background(), seedingTimeout())
	defer cancel()
	if err := tr.Run(ctx, res, w, noopBudget{}); err != nil {
		tr.Close()
		return nil, nil, err
	}
	return tr, w.Bytes(), nil
}

// ---- aria2-next leecher ----------------------------------------------------

func startAria2Leecher(bin, dir, announce, torrentPath string) *seeder {
	log, _ := os.OpenFile(filepath.Join(dir, "aria2-leech.log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	cmd := exec.Command(bin,
		"--dir="+dir,
		"--seed-time=0",
		"--seed-ratio=0.0",
		"--enable-dht=false",
		"--enable-dht6=false",
		"--enable-peer-exchange=false",
		"--disable-ipv6=true",
		"--bt-tracker="+announce,
		"--check-certificate=false",
		"--console-log-level=debug",
		"--summary-interval=0",
		torrentPath,
	)
	cmd.Stdout = log
	cmd.Stderr = log
	if err := cmd.Start(); err != nil {
		_ = log.Close()
		return &seeder{log: log}
	}
	return &seeder{cmd: cmd, log: log}
}

// waitAria2LeecherPayload uses the exact sha256 as the completion signal;
// a preallocated or partial file can never match.
func waitAria2LeecherPayload(dir, want string, timeout time.Duration) (int, bool) {
	path := filepath.Join(dir, "payload.bin")
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(path); err == nil && sha256Hex(b) == want {
			return len(b), true
		}
		time.Sleep(200 * time.Millisecond)
	}
	return 0, false
}

// ---- libtorrent leecher ----------------------------------------------------

type leecherResult struct {
	Status         string `json:"status"`
	Reason         string `json:"reason"`
	Engine         string `json:"engine"`
	Sha256         string `json:"sha256"`
	ExpectedSha256 string `json:"expected_sha256"`
	Bytes          int64  `json:"bytes"`
	State          string `json:"state"`
	TotalDownload  int64  `json:"total_download"`
	TotalUpload    int64  `json:"total_upload"`
	NumPeers       int    `json:"num_peers"`
	PeerPort       int    `json:"peer_port"`
	PeerID         string `json:"peer_id"`
}

func runLibtorrentLeecher(torrentPath, outDir string, udPort int, want string) (leecherResult, string, error) {
	script := envOr("UD_LIBTORRENT_LEECHER", seedingLeecherScriptDefault)
	if _, err := os.Stat(script); err != nil {
		return leecherResult{Status: "UNSUPPORTED", Reason: "leecher script unavailable: " + err.Error()}, "", nil
	}
	python := envOr("UD_LIBTORRENT_PYTHON", "python3")
	timeout := seedingTimeout()
	cmd := exec.Command(python, script,
		"--torrent", torrentPath,
		"--out-dir", outDir,
		"--peer-port", strconv.Itoa(udPort),
		"--expected-sha256", want,
		"--timeout", strconv.FormatFloat(timeout.Seconds(), 'f', 0, 64),
	)
	if extra := os.Getenv("UD_LIBTORRENT_PYTHONPATH"); extra != "" {
		cmd.Env = append(os.Environ(), "PYTHONPATH="+extra+string(os.PathListSeparator)+os.Getenv("PYTHONPATH"))
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	var line string
	for _, l := range strings.Split(stdout.String(), "\n") {
		if strings.TrimSpace(l) != "" {
			line = strings.TrimSpace(l)
			break
		}
	}
	if line == "" {
		return leecherResult{Status: "FAIL", Reason: "leecher produced no status line"}, tail(stderr.String(), 6), err
	}
	var r leecherResult
	if uerr := json.Unmarshal([]byte(line), &r); uerr != nil {
		return leecherResult{Status: "FAIL", Reason: "bad leecher status " + line}, tail(stderr.String(), 6), uerr
	}
	return r, tail(stderr.String(), 6), err
}

// ---- cases -----------------------------------------------------------------

func runSeedingCases(aria2 string) []verdict {
	_, err := os.Stat(aria2)
	available := err == nil
	return []verdict{
		runWithLoopbackRetry(func() verdict { return runSeedingAria2Case(aria2, available) }),
		runWithLoopbackRetry(func() verdict { return runSeedingLibtorrentCase() }),
	}
}

func seedingCaseSetup(size int) (payload []byte, tr *interop.HTTPTracker, torrentBytes []byte, ih string, seedDir string, err error) {
	payload = makePayload(size)
	tr = interop.NewHTTPTracker(false)
	torrentBytes = buildTorrent(payload, tr.URL())
	ih = infohashOf(torrentBytes)
	seedDir, err = os.MkdirTemp("", "udinteropx-seeding-")
	if err != nil {
		tr.Close()
		return nil, nil, nil, "", "", err
	}
	if err = os.WriteFile(filepath.Join(seedDir, "payload.bin"), payload, 0o644); err != nil {
		tr.Close()
		return nil, nil, nil, "", "", err
	}
	if err = os.WriteFile(filepath.Join(seedDir, "x.torrent"), torrentBytes, 0o644); err != nil {
		tr.Close()
		return nil, nil, nil, "", "", err
	}
	return payload, tr, torrentBytes, ih, seedDir, nil
}

func runSeedingAria2Case(aria2 string, available bool) verdict {
	const id = "BX-05"
	name := "ud seeds and aria2-next downloads over the real BT wire (hash-verified)"
	if !available {
		return verdict{ID: id, Client: clientName, Status: "SKIPPED", Name: name,
			Evidence: "aria2-next binary not present in this environment"}
	}
	payload, tr, torrentBytes, ih, seedDir, err := seedingCaseSetup(udSeedingPayloadSize)
	if err != nil {
		return verdict{ID: id, Client: clientName, Status: "SKIPPED", Name: name, Evidence: "setup failed: " + err.Error()}
	}
	defer tr.Close()
	defer os.RemoveAll(seedDir)
	announce := tr.URL()

	s := startAria2(aria2, seedDir, announce)
	defer s.stop()
	port := waitAria2ListenPort(filepath.Join(seedDir, "aria2.log"), 8*time.Second)
	if port == 0 {
		return verdict{ID: id, Client: clientName, Status: "FAIL", Name: name, InfoHash: ih,
			Error: "aria2-next did not bind a listen port"}
	}
	peer := &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: port}
	tr.SetPeers(interop.CompactPeer{IP: net.ParseIP("127.0.0.1"), Port: port})
	seederReady := waitSeederAnnounce(tr, port, 8*time.Second)

	udPort, err := freeLoopbackPort()
	if err != nil {
		return verdict{ID: id, Client: clientName, Status: "SKIPPED", Name: name, InfoHash: ih,
			Evidence: "cannot reserve ud listen port: " + err.Error()}
	}
	var ud *bt.Transport
	defer func() {
		if ud != nil {
			ud.Close()
		}
	}()
	ud, got, err := downloadAndSeedWithUD(torrentBytes, announce, int64(len(payload)), peer, udPort)
	if err != nil {
		return verdict{ID: id, Client: clientName, Status: "FAIL", Name: name, InfoHash: ih, Error: err.Error()}
	}
	if sha256Hex(got) != sha256Hex(payload) {
		return verdict{ID: id, Client: clientName, Status: "FAIL", Name: name, InfoHash: ih, Error: "ud download hash mismatch"}
	}

	// The original seeder is gone: the leecher can only receive bytes from ud.
	s.stop()
	tr.SetPeers(interop.CompactPeer{IP: net.ParseIP("127.0.0.1"), Port: udPort})
	udAddr := fmt.Sprintf("127.0.0.1:%d", udPort)
	if !waitTCP(udAddr, 10*time.Second) {
		return verdict{ID: id, Client: clientName, Status: "FAIL", Name: name, InfoHash: ih,
			Error: "ud seeding session never listened on " + udAddr}
	}

	leechDir, err := os.MkdirTemp("", "udinteropx-leech-")
	if err != nil {
		return verdict{ID: id, Client: clientName, Status: "SKIPPED", Name: name, InfoHash: ih,
			Evidence: "cannot create leecher dir: " + err.Error()}
	}
	defer os.RemoveAll(leechDir)
	_ = os.WriteFile(filepath.Join(leechDir, "x.torrent"), torrentBytes, 0o644)
	leecher := startAria2Leecher(aria2, leechDir, announce, filepath.Join(leechDir, "x.torrent"))
	defer leecher.stop()
	n, ok := waitAria2LeecherPayload(leechDir, sha256Hex(payload), seedingTimeout())
	if !ok {
		return verdict{ID: id, Client: clientName, Status: "FAIL", Name: name, InfoHash: ih,
			Error:    "aria2-next did not download the payload from ud seeding session",
			Evidence: tailLog(filepath.Join(leechDir, "aria2-leech.log"), 12)}
	}
	return verdict{ID: id, Client: clientName, Status: "PASS", Name: name, InfoHash: ih,
		Evidence: fmt.Sprintf("payload %d bytes sha256=%s; real aria2-next seeded first, ud downloaded with path A policy, seeder stopped, then a fresh aria2-next leecher downloaded %d bytes from ud at %s with sha256 verified; seeder BEP 3 announce(left=0) observed=%t; one-way loopback supplementary evidence, NOT L4",
			len(payload), sha256Hex(payload), n, udAddr, seederReady)}
}

func runSeedingLibtorrentCase() verdict {
	const id = "BX-06"
	name := "ud seeds and real libtorrent downloads over the real BT wire (hash-verified)"
	// A distinct payload size gives BX-06 its own infohash, so a session from
	// BX-05 can never be mistaken for this case's peer.
	payload, tr, torrentBytes, ih, seedDir, err := seedingCaseSetup(udSeedingPayloadSize + 16*1024)
	if err != nil {
		return verdict{ID: id, Client: ltFallbackClient, Status: "SKIPPED", Name: name, Evidence: "setup failed: " + err.Error()}
	}
	defer tr.Close()
	defer os.RemoveAll(seedDir)
	torrentPath := filepath.Join(seedDir, "x.torrent")
	announce := tr.URL()

	s, ready, err := startLtSeeder(seedDir, torrentPath)
	if err != nil {
		return verdict{ID: id, Client: ltFallbackClient, Status: "SKIPPED", Name: name, InfoHash: ih,
			Evidence: "libtorrent seeder not started: " + err.Error()}
	}
	defer s.stop()
	if ready.Status != "READY" {
		status := "SKIPPED"
		if ready.Status == "UNSUPPORTED" {
			status = "UNSUPPORTED"
		}
		return verdict{ID: id, Client: ltFallbackClient, Status: status, Name: name, InfoHash: ih,
			Evidence: fmt.Sprintf("libtorrent seeder unavailable (status=%s): %s", ready.Status, ready.Reason)}
	}
	client := ready.Engine
	if client == "" {
		client = ltFallbackClient
	}
	peer := &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: ready.Port}
	tr.SetPeers(interop.CompactPeer{IP: net.ParseIP("127.0.0.1"), Port: ready.Port})
	seederReady := waitSeederAnnounce(tr, ready.Port, 8*time.Second)
	if os.Getenv("UD_INTEROP_DEBUG") != "" {
		fmt.Fprintf(os.Stderr, "[debug] BX-06 lt seeder port=%d engine=%s state=%s announce=%s infohash=%s seederAnnounce=%t stderr=%s\n",
			ready.Port, client, ready.TorrentState, announce, ih, seederReady, s.stderrTail())
	}

	udPort, err := freeLoopbackPort()
	if err != nil {
		return verdict{ID: id, Client: client, Status: "SKIPPED", Name: name, InfoHash: ih,
			Evidence: "cannot reserve ud listen port: " + err.Error()}
	}
	var ud *bt.Transport
	defer func() {
		if ud != nil {
			ud.Close()
		}
	}()
	ud, got, err := downloadAndSeedWithUD(torrentBytes, announce, int64(len(payload)), peer, udPort)
	if err != nil {
		return verdict{ID: id, Client: client, Status: "FAIL", Name: name, InfoHash: ih,
			Error: err.Error(), Evidence: "seeder stderr: " + s.stderrTail()}
	}
	if sha256Hex(got) != sha256Hex(payload) {
		return verdict{ID: id, Client: client, Status: "FAIL", Name: name, InfoHash: ih, Error: "ud download hash mismatch"}
	}

	s.stop()
	tr.SetPeers(interop.CompactPeer{IP: net.ParseIP("127.0.0.1"), Port: udPort})
	udAddr := fmt.Sprintf("127.0.0.1:%d", udPort)
	if !waitTCP(udAddr, 10*time.Second) {
		return verdict{ID: id, Client: client, Status: "FAIL", Name: name, InfoHash: ih,
			Error: "ud seeding session never listened on " + udAddr}
	}

	leechDir, err := os.MkdirTemp("", "udinteropx-lt-leech-")
	if err != nil {
		return verdict{ID: id, Client: client, Status: "SKIPPED", Name: name, InfoHash: ih,
			Evidence: "cannot create leecher dir: " + err.Error()}
	}
	defer os.RemoveAll(leechDir)
	leecher, leecherStderr, runErr := runLibtorrentLeecher(torrentPath, leechDir, udPort, sha256Hex(payload))
	switch leecher.Status {
	case "PASS":
		return verdict{ID: id, Client: leecher.Engine, Status: "PASS", Name: name, InfoHash: ih,
			Evidence: fmt.Sprintf("payload %d bytes sha256=%s; real %s seeded first, ud downloaded with path A policy, seeder stopped, then a fresh libtorrent leecher downloaded %d bytes from ud at %s (state=%s, total_download=%d, num_peers=%d) and sha256 verified; seeder BEP 3 announce(left=0) observed=%t; one-way loopback supplementary evidence, NOT L4",
				len(payload), sha256Hex(payload), client, leecher.Bytes, udAddr, leecher.State, leecher.TotalDownload, leecher.NumPeers, seederReady)}
	case "UNSUPPORTED":
		return verdict{ID: id, Client: ltFallbackClient, Status: "UNSUPPORTED", Name: name, InfoHash: ih,
			Evidence: leecher.Reason}
	default:
		detail := leecher.Reason
		if runErr != nil {
			detail += "; process: " + runErr.Error()
		}
		return verdict{ID: id, Client: leecher.Engine, Status: "FAIL", Name: name, InfoHash: ih,
			Error: detail, Evidence: "leecher stderr: " + leecherStderr}
	}
}

func tailLog(path string, n int) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return tail(string(b), n)
}
