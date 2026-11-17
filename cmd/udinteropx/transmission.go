package main

// Transmission peer matrix (docs/12 §3.1 BT-03 supplementary evidence).
//
// BT-03 is judged by a qBittorrent / libtorrent-family engine. This file adds a
// SEPARATE, supplementary bidirectional matrix driven by a real
// transmission-daemon (3.00) on the isolated lab peer host, so the BT-03 claim
// is not backed by a single client family. It NEVER changes the L4 gate: the
// official result still comes from `make l4-run` over BT-01..BT-14, and this
// report is written as `ud.peer-matrix/v1` with its own non-claim.
//
// Design notes (each of these was a real defect during bring-up):
//   - transmission-daemon 3.00 uses `-p/--port` for the RPC port and
//     `-P/--peerport` for the peer port (the reverse of the usual guess).
//   - `-a/--allowed` populates the RPC whitelist; without it the default
//     `127.0.0.1` blocks the harness host at 192.168.1.222.
//   - The remote command line contains the profile path, so `pkill -f <path>`
//     kills the SSH shell itself. Cleanup uses the recorded PID file only.

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// trStatus* mirror Transmission's tr_torrent_activity values.
const (
	trStatusStopped      = 0
	trStatusCheckWait    = 1
	trStatusChecking     = 2
	trStatusDownloadWait = 3
	trStatusDownloading  = 4
	trStatusSeedWait     = 5
	trStatusSeeding      = 6
)

// trTorrent is the subset of Transmission's torrent-get fields this harness
// relies on. PercentDone / DownloadedEver / TotalSize are the completion
// authority; Status distinguishes checking from real seeding/downloading.
type trTorrent struct {
	ID             int     `json:"id"`
	Name           string  `json:"name"`
	HashString     string  `json:"hashString"`
	PercentDone    float64 `json:"percentDone"`
	Status         int     `json:"status"`
	DownloadedEver int64   `json:"downloadedEver"`
	UploadedEver   int64   `json:"uploadedEver"`
	TotalSize      int64   `json:"totalSize"`
	PeersConnected int     `json:"peersConnected"`
	Error          int     `json:"error"`
	ErrorString    string  `json:"errorString"`
}

// trSeeding reports a fully verified torrent that is actively offering data.
// A torrent still checking (status 1/2) is NOT seeding even at 100%.
func trSeeding(t trTorrent) bool {
	return t.PercentDone >= 1 && (t.Status == trStatusSeeding || t.Status == trStatusSeedWait)
}

// trComplete reports a finished download with every byte accounted for.
func trComplete(t trTorrent) bool {
	return t.PercentDone >= 1 && t.TotalSize > 0 && t.DownloadedEver >= t.TotalSize
}

func trStatusName(s int) string {
	switch s {
	case trStatusStopped:
		return "stopped"
	case trStatusCheckWait:
		return "check-wait"
	case trStatusChecking:
		return "checking"
	case trStatusDownloadWait:
		return "download-wait"
	case trStatusDownloading:
		return "downloading"
	case trStatusSeedWait:
		return "seed-wait"
	case trStatusSeeding:
		return "seeding"
	default:
		return "unknown(" + strconv.Itoa(s) + ")"
	}
}

// ---- RPC client ----------------------------------------------------------

type trRPC struct {
	base    string
	session string
	client  *http.Client
}

func newTRRPC(host string, port int) *trRPC {
	return &trRPC{
		base: fmt.Sprintf("http://%s:%d/transmission/rpc", host, port),
		// The lab segment has no proxy; a proxy-aware client would turn every
		// RPC into a CONNECT to the NAT host and fail.
		client: &http.Client{
			Timeout:   20 * time.Second,
			Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true},
		},
	}
}

type trEnvelope struct {
	Result    string          `json:"result"`
	Arguments json.RawMessage `json:"arguments"`
}

// call performs one RPC round-trip. Transmission answers 409 with the session
// id until the client echoes it back, so a single retry is required and
// expected; any other non-200 answer is a hard error.
func (c *trRPC) call(method string, args map[string]interface{}, out interface{}) error {
	if args == nil {
		args = map[string]interface{}{}
	}
	body, err := json.Marshal(map[string]interface{}{"method": method, "arguments": args})
	if err != nil {
		return err
	}
	for attempt := 0; attempt < 2; attempt++ {
		req, err := http.NewRequest(http.MethodPost, c.base, bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		if c.session != "" {
			req.Header.Set("X-Transmission-Session-Id", c.session)
		}
		resp, err := c.client.Do(req)
		if err != nil {
			return err
		}
		payload, readErr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if readErr != nil {
			return readErr
		}
		if resp.StatusCode == http.StatusConflict {
			if sid := resp.Header.Get("X-Transmission-Session-Id"); sid != "" {
				c.session = sid
				continue
			}
			return fmt.Errorf("%s: 409 without a session id", method)
		}
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("%s: HTTP %d: %s", method, resp.StatusCode, truncate(string(payload), 200))
		}
		var env trEnvelope
		if err := json.Unmarshal(payload, &env); err != nil {
			return fmt.Errorf("%s: decode: %v: %s", method, err, truncate(string(payload), 200))
		}
		if env.Result != "success" {
			return fmt.Errorf("%s: result=%q", method, env.Result)
		}
		if out != nil && len(env.Arguments) > 0 {
			if err := json.Unmarshal(env.Arguments, out); err != nil {
				return fmt.Errorf("%s: decode arguments: %v", method, err)
			}
		}
		return nil
	}
	return fmt.Errorf("%s: session negotiation failed", method)
}

// add sends the metainfo inline (base64) so no file has to exist on the peer
// host filesystem for the add call itself.
func (c *trRPC) add(metainfo []byte, downloadDir string) (int, error) {
	var out struct {
		Added *struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
		} `json:"torrent-added"`
		Duplicate *struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
		} `json:"torrent-duplicate"`
	}
	err := c.call("torrent-add", map[string]interface{}{
		"metainfo":     base64.StdEncoding.EncodeToString(metainfo),
		"download-dir": downloadDir,
		"paused":       false,
	}, &out)
	if err != nil {
		return 0, err
	}
	if out.Added != nil {
		return out.Added.ID, nil
	}
	if out.Duplicate != nil {
		return out.Duplicate.ID, nil
	}
	return 0, fmt.Errorf("torrent-add returned neither added nor duplicate")
}

func (c *trRPC) get(hash string) (trTorrent, error) {
	var out struct {
		Torrents []trTorrent `json:"torrents"`
	}
	err := c.call("torrent-get", map[string]interface{}{
		"fields": []string{
			"id", "name", "hashString", "percentDone", "status",
			"downloadedEver", "uploadedEver", "totalSize", "peersConnected",
			"error", "errorString",
		},
	}, &out)
	if err != nil {
		return trTorrent{}, err
	}
	for _, t := range out.Torrents {
		if strings.EqualFold(t.HashString, hash) {
			return t, nil
		}
	}
	if len(out.Torrents) == 1 {
		return out.Torrents[0], nil
	}
	return trTorrent{}, fmt.Errorf("torrent %s not found", hash)
}

func (c *trRPC) verify(id int) error {
	return c.call("torrent-verify", map[string]interface{}{"ids": []int{id}}, nil)
}

// sessionUploaded is the daemon-wide uploaded byte counter; it is a secondary
// cross-check for the seeding half-direction.
func (c *trRPC) sessionUploaded() (int64, error) {
	var out struct {
		CurrentStats struct {
			UploadedBytes int64 `json:"uploadedBytes"`
		} `json:"current-stats"`
	}
	if err := c.call("session-stats", nil, &out); err != nil {
		return 0, err
	}
	return out.CurrentStats.UploadedBytes, nil
}

// ---- daemon lifecycle on the peer host -----------------------------------

type trPeer struct {
	e         *l4Env
	remoteDir string
	rpcPort   int
	peerPort  int
	client    *trRPC
}

// l4PeerPortFree reports whether a TCP port is unused on the peer host. The
// harness picks RPC / peer ports on the *peer* host, so a local net.Listen
// probe would be meaningless.
func l4PeerPortFree(e *l4Env, port int) bool {
	out, err := e.remote(fmt.Sprintf("ss -ltn 2>/dev/null | grep -c ':%d ' || true", port), 15*time.Second)
	if err != nil {
		return false
	}
	return strings.TrimSpace(out) == "0"
}

func (e *l4Env) startTransmission(tag string) (*trPeer, error) {
	dir := "/tmp/udl4-tr-" + e.caseID + "-" + tag + "-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	var rpcPort, peerPort int
	for attempt := 0; attempt < 12; attempt++ {
		base := 41000 + (int(time.Now().UnixNano()/1e6) % 7000)
		rpcPort, peerPort = base, base+1
		if l4PeerPortFree(e, rpcPort) && l4PeerPortFree(e, peerPort) {
			break
		}
		time.Sleep(120 * time.Millisecond)
		rpcPort, peerPort = 0, 0
	}
	if rpcPort == 0 {
		return nil, fmt.Errorf("no free RPC/peer port pair on the peer host")
	}
	p := &trPeer{e: e, remoteDir: dir, rpcPort: rpcPort, peerPort: peerPort}

	// `-p` is the RPC port and `-P` the peer port in Transmission 3.00.
	// `-a` whitelists the harness host; `-T` disables auth (isolated segment);
	// `-M -O -Y` keep the run tracker-only. `-GSR` prevents the default
	// ratio-2.0 limit from stopping a seeder mid-run.
	start := fmt.Sprintf(
		"rm -rf %s; mkdir -p %s/config %s/dl; "+
			"sh -c 'nohup transmission-daemon -f -g %s/config -w %s/dl -p %d -P %d -r 0.0.0.0 -a %s,127.0.0.1 -T -M -O -Y -GSR </dev/null >%s/daemon.log 2>&1 & echo $! >%s/pid' </dev/null >/dev/null 2>&1; "+
			"cat %s/pid",
		dir, dir, dir, dir, dir, rpcPort, peerPort, l4EnvOr("UD_L4_HOST_IP", "192.168.1.222"), dir, dir, dir)
	out, err := e.remote(start, 30*time.Second)
	if err != nil {
		return nil, fmt.Errorf("start transmission-daemon: %v: %s", err, truncate(out, 400))
	}
	p.client = newTRRPC(e.peerHost, rpcPort)

	deadline := time.Now().Add(25 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := p.client.sessionUploaded(); err == nil {
			return p, nil
		}
		time.Sleep(300 * time.Millisecond)
	}
	log, _ := e.remote("tail -5 "+dir+"/daemon.log", 10*time.Second)
	return nil, fmt.Errorf("transmission RPC did not answer on %s:%d; log: %s", e.peerHost, rpcPort, truncate(log, 400))
}

func (p *trPeer) stop() {
	if p == nil || p.e == nil {
		return
	}
	// PID-file cleanup only: a pkill pattern would match this very SSH command
	// line (it contains the profile path) and kill the shell.
	_, _ = p.e.remote(fmt.Sprintf(
		"kill $(cat %s/pid 2>/dev/null) 2>/dev/null; sleep 1; kill -9 $(cat %s/pid 2>/dev/null) 2>/dev/null; rm -rf %s",
		p.remoteDir, p.remoteDir, p.remoteDir), 30*time.Second)
}

// stagePayload puts the exact bytes into the daemon's download dir so a
// `torrent-verify` turns the torrent into a real seeder.
func (p *trPeer) stagePayload(localPath, name string) error {
	if _, err := p.e.remote("mkdir -p "+p.remoteDir+"/dl", 15*time.Second); err != nil {
		return fmt.Errorf("mkdir: %v", err)
	}
	return p.e.remotePut(localPath, filepath.Join(p.remoteDir, "dl", name))
}

func (p *trPeer) waitTorrent(hash string, timeout time.Duration, ok func(trTorrent) bool) (trTorrent, error) {
	deadline := time.Now().Add(timeout)
	var last trTorrent
	for time.Now().Before(deadline) {
		t, err := p.client.get(hash)
		if err == nil {
			last = t
			if t.Error != 0 {
				return t, fmt.Errorf("transmission torrent error %d: %s", t.Error, t.ErrorString)
			}
			if ok(t) {
				return t, nil
			}
		}
		time.Sleep(300 * time.Millisecond)
	}
	return last, fmt.Errorf("timeout after %s (last: %s %.1f%% have=%d/%d peers=%d)",
		timeout, trStatusName(last.Status), last.PercentDone*100,
		last.DownloadedEver, last.TotalSize, last.PeersConnected)
}

func trPeerAddr(p *trPeer) net.Addr { return mustResolveTCP(p.e.peerHost, p.peerPort) }

// ---- matrix --------------------------------------------------------------

// peerMatrixCase is one half-direction result. Status follows the same honesty
// rule as L4: only a hash-verified exchange is PASS.
type peerMatrixCase struct {
	ID       string                 `json:"id"`
	Status   string                 `json:"status"`
	Detail   string                 `json:"detail"`
	Evidence map[string]interface{} `json:"evidence"`
}

type peerMatrixReport struct {
	Schema      string           `json:"schema"`
	Client      string           `json:"client"`
	GeneratedAt string           `json:"generated_at"`
	Home        string           `json:"home"`
	Away        string           `json:"away"`
	Cases       []peerMatrixCase `json:"cases"`
	AllPass     bool             `json:"all_pass"`
	NonClaim    string           `json:"non_claim"`
}

const peerMatrixNonClaim = "补充证据：本报告只证明 transmission-daemon 3.00 在隔离段与 ud 完成双向真实交换，" +
	"不改变 BT-01..BT-14 判据、不替代 `make l4-run` 的官方 L4 结果；两个方向的文件字节均经 sha256 校验。"

// runPeerMatrix drives the supplementary third-client matrix. `client` is
// explicit so an unsupported name can never be reported as a pass.
func runPeerMatrix(client string) peerMatrixReport {
	rep := peerMatrixReport{
		Schema:      "ud.peer-matrix/v1",
		Client:      client,
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		NonClaim:    peerMatrixNonClaim,
	}
	if client != "transmission" {
		rep.Cases = append(rep.Cases, peerMatrixCase{
			ID:     "MX-" + strings.ToUpper(client) + "-0",
			Status: "UNSUPPORTED",
			Detail: "本工作包只实装 transmission 矩阵；其余客户端未执行，不伪造 PASS",
			Evidence: map[string]interface{}{
				"requested_client": client,
			},
		})
		return rep
	}
	rep.Client = "transmission-daemon 3.00 (bb6b5a062e)"

	e, err := newL4Env("MX-TR")
	if err != nil {
		rep.Cases = append(rep.Cases, peerMatrixCase{ID: "MX-TR-0", Status: "FAIL",
			Detail: "lab environment unavailable: " + err.Error(), Evidence: nil})
		return rep
	}
	defer e.close()
	rep.Home = l4EnvOr("UD_L4_HOST_IP", "192.168.1.222")
	rep.Away = e.peerHost
	if err := e.startTracker(); err != nil {
		rep.Cases = append(rep.Cases, peerMatrixCase{ID: "MX-TR-0", Status: "FAIL",
			Detail: "real tracker unavailable: " + err.Error(), Evidence: e.ev()})
		return rep
	}

	payload := l4Payload(256 * 1024)
	want := l4SHA256(payload)
	torrent, err := l4BuildTorrent(payload, "mxtr.bin", e.announce, false)
	if err != nil {
		rep.Cases = append(rep.Cases, peerMatrixCase{ID: "MX-TR-0", Status: "FAIL",
			Detail: "build torrent: " + err.Error(), Evidence: e.ev()})
		return rep
	}
	ih := l4InfoHash(torrent)
	torrentPath := filepath.Join(e.work, "mxtr.torrent")
	payloadPath := filepath.Join(e.work, "mxtr.bin")
	if err := os.WriteFile(torrentPath, torrent, 0o644); err != nil {
		rep.Cases = append(rep.Cases, peerMatrixCase{ID: "MX-TR-0", Status: "FAIL",
			Detail: "write torrent: " + err.Error(), Evidence: e.ev()})
		return rep
	}
	if err := os.WriteFile(payloadPath, payload, 0o644); err != nil {
		rep.Cases = append(rep.Cases, peerMatrixCase{ID: "MX-TR-0", Status: "FAIL",
			Detail: "write payload: " + err.Error(), Evidence: e.ev()})
		return rep
	}

	// Half A: real Transmission seeds, ud downloads.
	seed, err := e.startTransmission("seed")
	if err != nil {
		rep.Cases = append(rep.Cases, peerMatrixCase{ID: "MX-TR-A", Status: "UNSUPPORTED",
			Detail: "real transmission-daemon unavailable: " + err.Error(), Evidence: e.ev()})
		return rep
	}
	defer seed.stop()
	if err := seed.stagePayload(payloadPath, "mxtr.bin"); err != nil {
		rep.Cases = append(rep.Cases, peerMatrixCase{ID: "MX-TR-A", Status: "FAIL",
			Detail: "stage seed payload: " + err.Error(), Evidence: e.ev()})
		return rep
	}
	if _, err := seed.client.add(torrent, seed.remoteDir+"/dl"); err != nil {
		rep.Cases = append(rep.Cases, peerMatrixCase{ID: "MX-TR-A", Status: "FAIL",
			Detail: "transmission add torrent: " + err.Error(), Evidence: e.ev()})
		return rep
	}
	if t, err := seed.client.get(ih); err == nil {
		_ = seed.client.verify(t.ID)
	}
	seedState, err := seed.waitTorrent(ih, 90*time.Second, trSeeding)
	if err != nil {
		rep.Cases = append(rep.Cases, peerMatrixCase{ID: "MX-TR-A", Status: "FAIL",
			Detail: "transmission did not become a seeder: " + err.Error(), Evidence: e.ev()})
		return rep
	}

	got, err := l4DownloadWithUD(torrent, int64(len(payload)), trPeerAddr(seed), false, 180*time.Second)
	evA := e.ev()
	evA["infohash"] = ih
	evA["bytes"] = len(payload)
	evA["seeder_status"] = trStatusName(seedState.Status)
	evA["seeder_percent_done"] = seedState.PercentDone
	evA["seeder_port"] = seed.peerPort
	if err != nil {
		rep.Cases = append(rep.Cases, peerMatrixCase{ID: "MX-TR-A", Status: "FAIL",
			Detail: "ud download from real Transmission: " + err.Error(), Evidence: evA})
		return rep
	}
	evA["sha256_expected"] = want
	evA["sha256_actual"] = l4SHA256(got)
	if l4SHA256(got) != want {
		rep.Cases = append(rep.Cases, peerMatrixCase{ID: "MX-TR-A", Status: "FAIL",
			Detail: "sha256 mismatch on the download half-direction", Evidence: evA})
		return rep
	}
	// The seeder's own counter must show the bytes it just handed us.
	if up, err := seed.client.get(ih); err == nil {
		evA["seeder_uploaded_ever"] = up.UploadedEver
		if up.UploadedEver < int64(len(payload)) {
			rep.Cases = append(rep.Cases, peerMatrixCase{ID: "MX-TR-A", Status: "FAIL",
				Detail:   fmt.Sprintf("transmission reports uploadedEver=%d < payload=%d", up.UploadedEver, len(payload)),
				Evidence: evA})
			return rep
		}
	}
	rep.Cases = append(rep.Cases, peerMatrixCase{ID: "MX-TR-A", Status: "PASS",
		Detail:   "真实 Transmission 做种 → ud 下载完成，sha256 一致且做种端 uploadedEver 覆盖载荷",
		Evidence: evA})

	// Half B: ud seeds (ADR-0021 path A), original seeder stops, a FRESH
	// Transmission instance downloads from us.
	obs := &l4SeedObserver{}
	listenPort, err := l4FreePort()
	if err != nil {
		rep.Cases = append(rep.Cases, peerMatrixCase{ID: "MX-TR-B", Status: "FAIL",
			Detail: "choose listen port: " + err.Error(), Evidence: e.ev()})
		return rep
	}
	tr, _, err := l4DownloadAndSeed(torrent, int64(len(payload)), trPeerAddr(seed), listenPort,
		l4LabSeedingPolicy(listenPort, 0, 30*time.Minute), obs, 180*time.Second)
	if err != nil {
		rep.Cases = append(rep.Cases, peerMatrixCase{ID: "MX-TR-B", Status: "FAIL",
			Detail: "ud download-with-seeding: " + err.Error(), Evidence: e.ev()})
		return rep
	}
	defer tr.Close()
	if !l4WaitSeedStarted(obs, 20*time.Second) {
		rep.Cases = append(rep.Cases, peerMatrixCase{ID: "MX-TR-B", Status: "FAIL",
			Detail: "ud seeding session did not start", Evidence: e.ev()})
		return rep
	}
	seed.stop() // after this the only live seeder is ud

	leech, err := e.startTransmission("leech")
	if err != nil {
		rep.Cases = append(rep.Cases, peerMatrixCase{ID: "MX-TR-B", Status: "UNSUPPORTED",
			Detail: "fresh transmission-daemon unavailable: " + err.Error(), Evidence: e.ev()})
		return rep
	}
	defer leech.stop()
	if _, err := leech.client.add(torrent, leech.remoteDir+"/dl"); err != nil {
		rep.Cases = append(rep.Cases, peerMatrixCase{ID: "MX-TR-B", Status: "FAIL",
			Detail: "leecher add torrent: " + err.Error(), Evidence: e.ev()})
		return rep
	}
	leechState, err := leech.waitTorrent(ih, 180*time.Second, trComplete)
	evB := e.ev()
	evB["infohash"] = ih
	evB["bytes"] = len(payload)
	evB["leecher_port"] = leech.peerPort
	if err != nil {
		_, uploaded, _, _, _, _ := obs.snapshot()
		evB["ud_bytes_uploaded"] = uploaded
		rep.Cases = append(rep.Cases, peerMatrixCase{ID: "MX-TR-B", Status: "FAIL",
			Detail: "real Transmission leecher did not complete: " + err.Error(), Evidence: evB})
		return rep
	}
	evB["leecher_status"] = trStatusName(leechState.Status)
	evB["leecher_percent_done"] = leechState.PercentDone
	evB["leecher_downloaded_ever"] = leechState.DownloadedEver
	uploaded, uploadSeries := l4WaitUploaded(obs, int64(len(payload)), 45*time.Second)
	evB["ud_bytes_uploaded"] = uploaded
	evB["ud_bytes_uploaded_samples"] = uploadSeries
	if uploaded < int64(len(payload)) {
		rep.Cases = append(rep.Cases, peerMatrixCase{ID: "MX-TR-B", Status: "FAIL",
			Detail: fmt.Sprintf("ud upload counter %d < payload %d", uploaded, len(payload)), Evidence: evB})
		return rep
	}
	// The strongest check: the file the leecher wrote on the peer host must
	// hash to the original payload.
	remoteSHA, err := e.remote("sha256sum "+filepath.Join(leech.remoteDir, "dl", "mxtr.bin"), 30*time.Second)
	evB["remote_sha256_output"] = strings.TrimSpace(remoteSHA)
	if err != nil {
		rep.Cases = append(rep.Cases, peerMatrixCase{ID: "MX-TR-B", Status: "FAIL",
			Detail: "sha256sum on the peer host: " + err.Error(), Evidence: evB})
		return rep
	}
	gotRemote := strings.Fields(strings.TrimSpace(remoteSHA))
	if len(gotRemote) == 0 || !strings.EqualFold(gotRemote[0], want) {
		evB["sha256_expected"] = want
		rep.Cases = append(rep.Cases, peerMatrixCase{ID: "MX-TR-B", Status: "FAIL",
			Detail: "peer-host file sha256 mismatch", Evidence: evB})
		return rep
	}
	evB["sha256_expected"] = want
	evB["sha256_actual"] = gotRemote[0]
	rep.Cases = append(rep.Cases, peerMatrixCase{ID: "MX-TR-B", Status: "PASS",
		Detail:   "ud 做种（ADR-0021 路径 A）→ 全新 Transmission 下载完成，端侧文件 sha256 一致且我方上传计数覆盖载荷",
		Evidence: evB})

	rep.AllPass = true
	for _, c := range rep.Cases {
		if c.Status != "PASS" {
			rep.AllPass = false
		}
	}
	return rep
}
