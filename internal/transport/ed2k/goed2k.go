package ed2ktransport

// Production eD2k engine adapter (WBS 16 / ADR-0030 / docs/25 §3).
//
// It drives the forked goed2k engine (internal/ed2k) with the same reader
// adapter pattern BitTorrent uses: the engine downloads into a controlled
// output dir, then Run reads the COMPLETE file back through a seekable reader
// and charges every byte to ud's single Budget. The engine's own rate limiter
// and AutoSave persistence are disabled so ud stays the unique rate/state
// authority (ADR-0002).
//
// Network I/O only happens when Open is called; construction, Probe and the
// engine factory seam are offline, so CI never touches the real eD2k network.

import (
	"context"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/asdfgh129/gobitmule/internal/core"
	goed2k "github.com/asdfgh129/gobitmule/internal/ed2k"
	"github.com/asdfgh129/gobitmule/internal/ed2k/protocol"
)

// transferHandle is the narrow view of a completed transfer the adapter needs.
// goed2k.TransferHandle satisfies it natively; tests supply a fake with a
// temp-file reader so CI stays offline.
type transferHandle interface {
	IsFinished() bool
	GetFile() *os.File
}

// statusHandle is implemented by the real goed2k.TransferHandle. Keeping it
// optional lets offline fakes that only care about file/is-finished stay tiny.
type statusHandle interface {
	GetStatus() goed2k.TransferStatus
}

// reportEngineProgress samples the engine's byte counter and forwards it. It
// returns the sampled value so callers can log changes without a second call.
//
// The sample is TotalReceived (committed bytes plus the partially received
// block), not TotalDone. A 190 KiB block only commits every few seconds — tens
// of seconds on a slow swarm — so sampling TotalDone makes the counter advance
// once per block and the Web console's speed alternate between one spike and
// zero. TotalReceived moves on every SendingPart fragment, which is the same
// granularity eMule shows as 已下载.
func reportEngineProgress(report func(int64), h transferHandle) (int64, bool) {
	if h == nil {
		return 0, false
	}
	sh, ok := h.(statusHandle)
	if !ok {
		return 0, false
	}
	status := sh.GetStatus()
	done := status.TotalReceived
	if done < status.TotalDone {
		// Defensive: a resume load can briefly report committed bytes before the
		// inflight counters are rebuilt.
		done = status.TotalDone
	}
	if report != nil {
		report(done)
	}
	return done, true
}

// reportEngineUpload samples the engine's served-to-peers byte counter. It is
// separate from the download counter on purpose: a task can upload while its
// download is stalled, and conflating the two hid that entirely.
func reportEngineUpload(upload func(int64), h transferHandle) (int64, bool) {
	if upload == nil || h == nil {
		return 0, false
	}
	sh, ok := h.(statusHandle)
	if !ok {
		return 0, false
	}
	served := sh.GetStatus().Upload
	if served < 0 {
		served = 0
	}
	upload(served)
	return served, true
}

// statefulEngine is implemented by the real goed2k engine. The engine owns the
// .part file and writes blocks itself, so only it can tell a later run which
// pieces are already on disk. Without this the adapter restarts every transfer
// from byte 0 after a daemon restart, and the console's progress goes backwards.
// Kept optional so offline fakes stay tiny.
type statefulEngine interface {
	SetStatePath(path string)
	LoadState(path string) error
}

// engine is the narrow surface Goed2kDownloader needs from the forked client.
type engine interface {
	Start() error
	LoadDHTNodesDat(path ...string) error
	ConnectServerMet(path string) error
	AddLink(linkValue, outputDir string) (transferHandle, string, error)
	Wait() error
	Stop() error
}

// realEngine wraps *goed2k.Client to satisfy engine.
type realEngine struct{ c *goed2k.Client }

func (e *realEngine) Start() error                      { return e.c.Start() }
func (e *realEngine) LoadDHTNodesDat(p ...string) error { return e.c.LoadDHTNodesDat(p...) }
func (e *realEngine) ConnectServerMet(p string) error   { return e.c.ConnectServerMet(p) }
func (e *realEngine) Wait() error                       { return e.c.Wait() }
func (e *realEngine) Stop() error {
	defer goed2k.UnregisterActiveClient(e.c)
	return e.c.Stop()
}
func (e *realEngine) AddLink(link, out string) (transferHandle, string, error) {
	h, path, err := e.c.AddLink(link, out)
	return h, path, err // goed2k.TransferHandle satisfies transferHandle
}
func (e *realEngine) SetStatePath(path string)    { e.c.SetStatePath(path) }
func (e *realEngine) LoadState(path string) error { return e.c.LoadState(path) }

// engineFactory builds an engine per Downloader Open. Production returns a
// configured *realEngine; tests inject a fake factory (zero network).
type engineFactory func() (engine, error)

// Goed2kConfig carries the engine settings needed to join the eD2k network.
// KAD nodes.dat / server.met are explicit (ADR-0030 / ED-02); ListenPort/UDPPort
// mirror the lab peer ports (209 eMule uses 31969 / 41296).
type Goed2kConfig struct {
	ListenPort      int
	UDPPort         int
	OutputDir       string       // controlled dir for completed files
	NodesDat        string       // optional KAD bootstrap file
	ServerMet       string       // optional eD2k server list
	DisableRate     bool         // zero the engine's own upload limiter (ADR-0002)
	DisableAutoSave bool         // zero AutoSaveInterval (ADR-0002)
	Obfuscation     bool         // accept eMule basic obfuscation on incoming peers (ED-05)
	Logger          *slog.Logger // optional: route engine logx into the daemon log
	// StateDir holds one engine resume file per transfer hash. It carries the
	// piece bitmap of what the engine already wrote to the .part file, which is
	// the only thing that survives a restart; ud keeps owning task state and
	// chunk accounting. Empty disables resume (every run starts from byte 0).
	StateDir string
}

// Goed2kDownloader implements Downloader by driving the forked goed2k engine.
type Goed2kDownloader struct {
	cfg     Goed2kConfig
	factory engineFactory
}

// NewGoed2kDownloader builds the production Downloader.
func NewGoed2kDownloader(cfg Goed2kConfig) *Goed2kDownloader {
	return &Goed2kDownloader{cfg: cfg, factory: defaultEngineFactory(cfg)}
}

// newGoed2kDownloaderWithFactory is the offline test seam.
func newGoed2kDownloaderWithFactory(cfg Goed2kConfig, f engineFactory) *Goed2kDownloader {
	return &Goed2kDownloader{cfg: cfg, factory: f}
}

// restoreUserIdentity gives the engine back the user hash it used last run,
// before it can reach a server.
//
// Every state file in StateDir carries the same identity, so reading any one of
// them is enough; transfers themselves are recovered by the scheduler, not by
// this path. Without this the engine generates a fresh hash at construction and
// logs in with it, so the very first login of every process is an unknown
// client: the server registers a stranger and every peer's upload queue treats
// us as someone with no credit.
func restoreUserIdentity(c *goed2k.Client, cfg Goed2kConfig) {
	if c == nil || cfg.StateDir == "" {
		return
	}
	paths, err := filepath.Glob(filepath.Join(cfg.StateDir, "*.json"))
	if err != nil || len(paths) == 0 {
		return
	}
	for _, path := range paths {
		hash, err := goed2k.ReadUserHashFromStateFile(path)
		if err != nil || hash.Equal(protocol.Invalid) {
			continue
		}
		if err := c.SetUserHash(hash); err != nil {
			continue
		}
		if cfg.Logger != nil {
			cfg.Logger.Debug("ed2k identity restored", "userHash", hash.String(), "from", filepath.Base(path))
		}
		return
	}
}

// defaultEngineFactory constructs a freshly configured goed2k Client.
// DisableRate/DisableAutoSave default true in NewGoed2kConfig semantics below;
// the zero value here also means "disable" so ud stays the single authority.
func defaultEngineFactory(cfg Goed2kConfig) engineFactory {
	return func() (engine, error) {
		st := goed2k.NewSettings()
		if cfg.ListenPort > 0 {
			st.ListenPort = cfg.ListenPort
		}
		if cfg.UDPPort > 0 {
			st.UDPPort = cfg.UDPPort
		}
		st.EnableDHT = cfg.NodesDat != ""
		st.EnableObfuscation = cfg.Obfuscation // ED-05: opt-in via config.ED2K.Obfuscation
		st.Logger = cfg.Logger                 // surface source discovery / KAD failures
		st.MaxUploadRateKB = 0                 // ud is the unique rate authority (ADR-0002)
		c := goed2k.NewClient(st)
		restoreUserIdentity(c, cfg)
		// AutoSave is the engine's periodic state write. It stays off unless the
		// adapter owns a resume dir: ud must not get a second task-state writer
		// (ADR-0002), but the piece bitmap of the .part file exists nowhere else,
		// so a resumable run has to persist it. 10s bounds the loss to one window.
		if cfg.StateDir != "" {
			c.SetAutoSaveInterval(10 * time.Second)
		} else {
			c.SetAutoSaveInterval(0)
		}
		goed2k.RegisterActiveClient(c)
		return &realEngine{c: c}, nil
	}
}

// Open drives the engine to completion and returns a seekable reader over the
// completed file. It is the only method that touches the network.
func (d *Goed2kDownloader) Open(ctx context.Context, r *core.Resource) (io.ReadSeekCloser, int64, error) {
	return d.open(ctx, r, nil, nil)
}

// OpenWithProgress is Open plus a periodic engine byte counter. The eD2k
// engine writes into its own piece manager before the reader adapter can copy
// chunks, so without this hook the REST task would stay at bytesDone=0 until
// the whole transfer finished.
func (d *Goed2kDownloader) OpenWithProgress(ctx context.Context, r *core.Resource,
	report func(done int64)) (io.ReadSeekCloser, int64, error) {
	return d.open(ctx, r, report, nil)
}

// OpenWithProgressAndUpload adds the engine's upload counter. eD2k serves the
// parts it already holds while it downloads, so a task can be uploading long
// before it is finished; reporting it is what makes the console's upload column
// and the queue-priority story visible.
func (d *Goed2kDownloader) OpenWithProgressAndUpload(ctx context.Context, r *core.Resource,
	report func(done int64), upload func(uploaded int64)) (io.ReadSeekCloser, int64, error) {
	return d.open(ctx, r, report, upload)
}

func (d *Goed2kDownloader) open(ctx context.Context, r *core.Resource,
	report func(done int64), upload func(uploaded int64)) (io.ReadSeekCloser, int64, error) {
	if d == nil || d.factory == nil {
		return nil, 0, core.Err(core.E_CAPABILITY_UNAVAILABLE, "goed2k engine factory is not configured")
	}
	if d.cfg.OutputDir == "" {
		return nil, 0, core.Err(core.E_CAPABILITY_UNAVAILABLE, "goed2k output directory is not configured")
	}
	size, name, err := primaryFile(r)
	if err != nil {
		return nil, 0, err
	}
	if len(r.Identity.ED2KFileHash) != 32 {
		return nil, 0, core.Err(core.E_INVALID_INPUT, "ed2k resource has no valid MD4 file hash")
	}
	link := buildFileLink(name, size, r.Identity.ED2KFileHash)

	eng, err := d.factory()
	if err != nil {
		return nil, 0, core.Err(core.E_TRANSIENT_NETWORK, "goed2k engine init failed: %v", err)
	}
	if d.cfg.NodesDat != "" {
		if err := eng.LoadDHTNodesDat(d.cfg.NodesDat); err != nil {
			_ = eng.Stop()
			return nil, 0, core.Err(core.E_TRANSIENT_NETWORK, "KAD nodes.dat load failed: %v", err)
		}
	}
	if d.cfg.ServerMet != "" {
		if err := eng.ConnectServerMet(d.cfg.ServerMet); err != nil {
			_ = eng.Stop()
			return nil, 0, core.Err(core.E_TRANSIENT_NETWORK, "server.met connect failed: %v", err)
		}
	}
	if err := eng.Start(); err != nil {
		_ = eng.Stop()
		return nil, 0, core.Err(core.E_TRANSIENT_NETWORK, "goed2k start failed: %v", err)
	}
	// Restore the engine's own progress before registering the link. The resume
	// file only carries the piece bitmap of this transfer; the task state and
	// the chunk ledger stay in ud (ADR-0002). Loading before AddLink makes
	// AddLink return the already-restored transfer instead of a fresh one.
	if d.cfg.StateDir != "" {
		if se, ok := eng.(statefulEngine); ok {
			statePath := filepath.Join(d.cfg.StateDir, strings.ToLower(r.Identity.ED2KFileHash)+".json")
			se.SetStatePath(statePath)
			if lerr := se.LoadState(""); lerr != nil && d.cfg.Logger != nil {
				d.cfg.Logger.Warn("ed2k resume state load failed; starting from zero",
					"path", statePath, "error", lerr)
			}
		}
	}
	handle, _, err := eng.AddLink(link, d.cfg.OutputDir)
	if err != nil {
		_ = eng.Stop()
		return nil, 0, core.Err(core.E_TRANSIENT_NETWORK, "goed2k AddLink failed: %v", err)
	}

	lastProgress, _ := reportEngineProgress(report, handle)
	lastUploaded, _ := reportEngineUpload(upload, handle)
	// Say out loud where the transfer starts. Without this line a resume and a
	// from-scratch run look identical in the log, which is exactly the ambiguity
	// that made "progress went backwards" hard to pin down.
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("ed2k transfer started", "file", name,
			"resumedBytes", lastProgress, "size", size)
	}
	// Log the first sample even when it is 0: that line distinguishes "engine
	// started, no bytes yet" from "engine never reached the sampling loop" when
	// a task looks stuck at 0 bytes.
	logged := false

	// Wait() has no ctx; run it in a goroutine so cancellation is honored.
	waitErr := make(chan error, 1)
	go func() { waitErr <- eng.Wait() }()

	// Sample the engine while it runs: this byte counter is the only progress
	// signal available before the whole file completes, so it feeds both the
	// scheduler overlay (report) and the debug log (change-only, 1s cadence).
	var tickCh <-chan time.Time
	if report != nil || upload != nil || d.cfg.Logger != nil {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		tickCh = ticker.C
	}
waitLoop:
	for {
		select {
		case <-ctx.Done():
			_ = eng.Stop()
			return nil, 0, ctx.Err()
		case err := <-waitErr:
			if err != nil {
				_ = eng.Stop()
				return nil, 0, core.Err(core.E_TRANSIENT_NETWORK, "goed2k wait failed: %v", err)
			}
			break waitLoop
		case <-tickCh:
			if served, ok := reportEngineUpload(upload, handle); ok && served != lastUploaded {
				lastUploaded = served
				if d.cfg.Logger != nil {
					d.cfg.Logger.Debug("ed2k engine upload", "bytesUploaded", served)
				}
			}
			done, ok := reportEngineProgress(report, handle)
			if ok && (!logged || done != lastProgress) {
				logged, lastProgress = true, done
				if d.cfg.Logger != nil {
					d.cfg.Logger.Debug("ed2k engine progress", "bytesDone", done)
				}
			}
		}
	}
	reportEngineProgress(report, handle)
	reportEngineUpload(upload, handle)

	if !handle.IsFinished() {
		_ = eng.Stop()
		return nil, 0, core.Err(core.E_TRANSIENT_NETWORK, "goed2k transfer not finished after Wait")
	}
	f := handle.GetFile()
	if f == nil {
		_ = eng.Stop()
		return nil, 0, core.Err(core.E_TRANSIENT_NETWORK, "goed2k completed file has no reader")
	}
	// The completed file stays on disk after engine Stop; reopen by path so the
	// reader is independent of the engine's own *os.File lifecycle.
	path := f.Name()
	_ = eng.Stop()
	rf, err := os.Open(path)
	if err != nil {
		return nil, 0, core.Err(core.E_TRANSIENT_NETWORK, "reopen completed eD2k file failed: %v", err)
	}
	return rf, size, nil
}

// buildFileLink assembles a canonical ed2k://|file|name|size|hash|/ link from
// the frozen Resource fields. QueryEscape protects the name delimiter.
func buildFileLink(name string, size int64, md4hex string) string {
	return "ed2k://|file|" + url.QueryEscape(strings.TrimSpace(name)) +
		"|" + strconv.FormatInt(size, 10) + "|" + strings.ToLower(md4hex) + "|/"
}

var _ Downloader = (*Goed2kDownloader)(nil)
