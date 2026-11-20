// Package app assembles the daemon from all internal packages and owns its
// lifecycle. Both `udd` and `udctl daemon` call Run (docs/07 §3).
package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/asdfgh129/ud/internal/api"
	"github.com/asdfgh129/ud/internal/config"
	"github.com/asdfgh129/ud/internal/core"
	"github.com/asdfgh129/ud/internal/plugin"
	"github.com/asdfgh129/ud/internal/postproc"
	"github.com/asdfgh129/ud/internal/resolve"
	"github.com/asdfgh129/ud/internal/scheduler"
	"github.com/asdfgh129/ud/internal/security"
	"github.com/asdfgh129/ud/internal/store"
	"github.com/asdfgh129/ud/internal/transport"
	bttransport "github.com/asdfgh129/ud/internal/transport/bt"
	dashtransport "github.com/asdfgh129/ud/internal/transport/dash"
	ftptransport "github.com/asdfgh129/ud/internal/transport/ftp"
	hlstransport "github.com/asdfgh129/ud/internal/transport/hls"
	httptransport "github.com/asdfgh129/ud/internal/transport/http"
	sftptransport "github.com/asdfgh129/ud/internal/transport/sftp"
	"github.com/asdfgh129/ud/internal/version"
)

// newBuiltinResolvers wires the P1 resolver chain. Order is a security
// invariant (docs/05 §10, docs/08 §5): security-sensitive / protocol-specific
// built-ins (Metalink, link wrappers, magnet, .torrent) are registered BEFORE
// any plugin, and Unsupported stays LAST so a broken registration chain
// produces an explicit error instead of a silent nil.
//
// WBS 10 nuance: a plugin rule must be consulted before `direct-http`, because
// DirectHTTP matches every http(s) URL and is the generic fallback, not a
// security-sensitive built-in. ADR-0014 §4 names thunder decoding, Metalink
// and the SSRF Guard as the non-overridable set — all of those stay ahead of
// plugins. Manifest.Validate additionally refuses match rules that could hit
// those schemes, so a plugin can never shadow them even if the order changes.
func newBuiltinResolvers(guard core.URLGuard, beforeDirect ...core.Resolver) *resolve.Registry {
	r := resolve.NewRegistry()
	r.Register(resolve.NewMetalinkResolver(guard))
	r.Register(resolve.NewWrapperResolver(guard))
	r.Register(resolve.NewMagnetResolver(guard))
	r.Register(resolve.NewTorrentResolver(guard))
	// WBS 13: playlist / MPD URLs must be classified before the plugin hook
	// so a site rule cannot accidentally swallow a direct .m3u8 / .mpd link
	// (docs/05 §10 keeps protocol-native resolvers ahead of plugins).
	r.Register(resolve.NewHLSResolver(guard))
	r.Register(resolve.NewDASHResolver(guard))
	for _, x := range beforeDirect {
		if x != nil {
			r.Register(x)
		}
	}
	r.Register(resolve.NewDirectHTTP(guard))
	r.Register(resolve.NewDirectFile(guard))
	r.Register(resolve.Unsupported{})
	return r
}

// newBuiltinResolversForConfig adds the optional, opt-in site resolver. It
// stays ahead of plugin rules (docs/05 §10) and is absent entirely when
// sources.ytdlp.enabled=false, so the zero-sidecar baseline is unchanged.
func newBuiltinResolversForConfig(guard core.URLGuard, cfg config.Config, extra ...core.Resolver) *resolve.Registry {
	site := make([]core.Resolver, 0, 1)
	if cfg.Sources.YTDLP.Enabled {
		site = append(site, resolve.NewYTDLP(guard, resolve.YTDLPOptions{
			Binary:  cfg.Sources.YTDLP.Binary,
			Timeout: cfg.Sources.YTDLP.EffectiveTimeout(),
			MaxJSON: int(cfg.Sources.YTDLP.EffectiveMaxBytes()),
		}))
	}
	return newBuiltinResolvers(guard, append(site, extra...)...)
}

// newBuiltinTransports wires the byte movers. HTTP stays first so the
// capabilities list is stable for existing clients (scheduler/extra_test.go).
// FTP/FTPS and SFTP were added in P1 WBS 9; both need the credential store
// because plaintext secrets must never be carried by a Resource. The BT
// Transport (WBS 7) also receives the credential store so private-trackers
// passkeys can be resolved without ever living on a Resource.
func newBuiltinTransports(guard core.URLGuard, creds core.CredentialStore, btOpts ...bttransport.Option) *transport.Registry {
	r := transport.NewRegistry()
	r.Register(httptransport.New(guard))
	r.Register(ftptransport.New(guard, creds))
	r.Register(sftptransport.New(guard, creds))
	r.Register(bttransport.New(guard, creds, btOpts...))
	// WBS 13 L1: HLS and DASH streaming. Supports() only claims
	// core.KindStream, so registration order does not change behaviour for
	// existing kinds.
	r.Register(hlstransport.New(guard))
	r.Register(dashtransport.New(guard))
	return r
}

type Options struct {
	ConfigPath string
	Listen     string
	Dir        string
	DBPath     string
	LogLevel   string
	// DiscoveryOverride redirects the local discovery file (used by tests so
	// they never touch the user's real config directory).
	DiscoveryOverride string
	// InsecurePlugins is the CLI --insecure-plugins escape hatch for local
	// rule development: it skips Ed25519 verification for this run only.
	InsecurePlugins bool
}

// Discovery is the local discovery file written when listen uses port 0
// (ADR-0016: never occupy a fixed 6800 port).
type Discovery struct {
	Addr      string `json:"addr"`
	PID       int    `json:"pid"`
	Version   string `json:"version"`
	StartedAt int64  `json:"startedAt"`
}

func DiscoveryPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = os.TempDir()
	}
	return filepath.Join(dir, "ud", "daemon.json")
}

func ReadDiscovery() (Discovery, error) {
	b, err := os.ReadFile(DiscoveryPath())
	if err != nil {
		return Discovery{}, err
	}
	var d Discovery
	if err := json.Unmarshal(b, &d); err != nil {
		return Discovery{}, err
	}
	return d, nil
}

// Run blocks until ctx is cancelled, then shuts down in order.
func Run(ctx context.Context, opts Options) error {
	cfgPath := opts.ConfigPath
	if cfgPath == "" {
		if _, err := os.Stat("config.yaml"); err == nil {
			cfgPath = "config.yaml"
		}
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}
	if opts.Listen != "" {
		cfg.Listen = opts.Listen
	}
	if opts.Dir != "" {
		cfg.DownloadDir = config.ExpandHome(opts.Dir)
	}
	if opts.LogLevel != "" {
		cfg.LogLevel = opts.LogLevel
	}
	if opts.InsecurePlugins {
		cfg.Plugins.Insecure = true
	}
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("invalid config: %w", err)
	}
	log := newLogger(cfg.LogLevel)
	if opts.InsecurePlugins {
		log.Warn("plugin signature verification disabled (--insecure-plugins); local development only")
	}

	dbPath := opts.DBPath
	if dbPath == "" {
		dir, err := os.UserConfigDir()
		if err != nil {
			dir = os.TempDir()
		}
		dbPath = filepath.Join(dir, "ud", "state.db")
	}
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
		return err
	}
	if err := os.MkdirAll(cfg.DownloadDir, 0o755); err != nil {
		return err
	}

	bus := core.NewEventBus(512)
	st, err := store.Open(dbPath, bus)
	if err != nil {
		return err
	}
	defer st.Close()

	// docs/06 §7: keep the audit log bounded. The janitor stops before
	// st.Close() because this defer is registered later (LIFO).
	janCtx, janCancel := context.WithCancel(ctx)
	janDone := make(chan struct{})
	janitor := newEventJanitor(st, cfg.Events, log)
	go func() {
		defer close(janDone)
		janitor.run(janCtx)
	}()
	defer func() {
		janCancel()
		<-janDone
	}()
	guard := security.NewSSRFGuard(cfg.Security)
	mgr := config.NewManager(cfg, nil)
	// ADR-0015 decision ④: credentials go to the OS secret store when the host
	// has one. The state directory is reused so the file/DPAPI backends keep
	// their payload next to state.db and survive a restart; the chosen backend is
	// logged and published through GET /capabilities (never inferred by callers).
	creds, err := security.NewCredentialStore(security.CredentialStoreOptions{
		Dir: filepath.Dir(dbPath),
	})
	if err != nil {
		return fmt.Errorf("credential store: %w", err)
	}
	log.Info("credential store ready", "backend", creds.CredentialBackend())

	// WBS 10: the rule channel is optional. With plugins.dir unset there is no
	// plugin Resolver in the chain at all, so the P0/P1 behaviour is unchanged.
	var pluginResolvers []core.Resolver
	if cfg.Plugins.Dir != "" {
		keys, err := plugin.ParsePublicKeys(cfg.Plugins.TrustedKeys)
		if err != nil {
			return fmt.Errorf("invalid plugins.trustedKeys: %w", err)
		}
		interval := 30 * time.Second
		if cfg.Plugins.ReloadInterval != "" {
			interval, err = time.ParseDuration(cfg.Plugins.ReloadInterval)
			if err != nil {
				return fmt.Errorf("invalid plugins.reloadInterval: %w", err)
			}
		}
		pm, err := plugin.New(plugin.Options{
			Dir:             cfg.Plugins.Dir,
			TrustedKeys:     keys,
			VerifySignature: cfg.Plugins.VerifySignature,
			Insecure:        cfg.Plugins.Insecure,
			Guard:           guard,
			Logger:          log,
			// RULE_OUTDATED must never silently drop a task (ADR-0014 §6).
			Fallback:       resolve.NewDirectHTTP(guard),
			ReloadInterval: interval,
		})
		if err != nil {
			return fmt.Errorf("load plugin rule channel: %w", err)
		}
		go pm.Watch(ctx)
		pluginResolvers = append(pluginResolvers, pm)
		log.Info("plugin rule channel enabled", "dir", cfg.Plugins.Dir,
			"active", pm.Active(), "reloadInterval", interval.String(),
			"verifySignature", cfg.Plugins.VerifySignature && !cfg.Plugins.Insecure)
	}

	resolvers := newBuiltinResolversForConfig(guard, cfg, pluginResolvers...)
	// ADR-0021 path A: bt.* keys are LIVE now. The BT transport owns bounded
	// background seeding sessions, tied to the daemon context, the persisted
	// tasks.bytes_uploaded counter and the Completed -> Seeding -> Completed
	// state transitions. Without these options it stays leech-only (lab/tests).
	btOpts := []bttransport.Option{
		bttransport.WithSeedingPolicy(bttransport.SeedingFromConfig(cfg.BT)),
		bttransport.WithSeedingObserver(newSeedingObserver(st, log)),
		bttransport.WithLifecycleContext(ctx),
	}
	transports := newBuiltinTransports(guard, creds, btOpts...)
	defer transports.Close()

	postprocs := postproc.NewRegistry()
	// ADR-0011/0012: ffmpeg is an opt-in desktop plugin. A missing or unsuitable
	// binary degrades to a warning; downloads and the built-in passthrough stay
	// available. Startup probes run once, never in the transfer hot path.
	if cfg.PostProcess.FFmpegEnabled {
		tp, perr := postproc.NewTranscode(ctx, postproc.Options{
			Binary:        cfg.PostProcess.FFmpegBinary,
			Threads:       cfg.PostProcess.FFmpegThreads,
			MaxConcurrent: cfg.PostProcess.FFmpegMaxConcurrent,
			Timeout:       cfg.PostProcess.EffectiveTimeout(),
		})
		if perr != nil {
			log.Warn("ffmpeg transcode unavailable; downloads continue", "error", perr)
		} else {
			postprocs.Register(tp)
			log.Info("ffmpeg transcode registered", "sidecar", tp.Sidecars())
		}
	}

	sch, err := scheduler.New(scheduler.Options{
		Store: st, Resolvers: resolvers, Transports: transports,
		PostProcessors: postprocs, Guard: guard, Credentials: creds,
		Config: mgr, Bus: bus, Logger: log,
	})
	if err != nil {
		return err
	}
	defer sch.Close()
	recovered, err := sch.Recover(ctx)
	if err != nil {
		return err
	}

	loopback := isLoopbackListen(cfg.Listen)
	srv := api.New(api.Options{
		Control: sch, Config: mgr, Credentials: creds, Bus: bus, Logger: log,
		EventHistory: st.Events,
		// Fail closed: any non-loopback listener requires a Bearer token.
		// config.Validate already rejects the token-less case at startup.
		AuthToken: cfg.RemoteToken, RequireAuth: !loopback,
	})
	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return fmt.Errorf("listen %s: %w", cfg.Listen, err)
	}
	addr := ln.Addr().String()
	dpath := opts.DiscoveryOverride
	if dpath == "" {
		dpath = DiscoveryPath()
	}
	if err := os.MkdirAll(filepath.Dir(dpath), 0o700); err == nil {
		b, _ := json.Marshal(Discovery{Addr: addr, PID: os.Getpid(),
			Version: version.String(), StartedAt: time.Now().UnixMilli()})
		_ = os.WriteFile(dpath, b, 0o600)
	}
	defer os.Remove(dpath)

	httpSrv := &http.Server{Handler: srv, ReadHeaderTimeout: 10 * time.Second}
	serveErr := make(chan error, 1)
	go func() {
		if err := httpSrv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
		}
	}()
	log.Info("daemon ready", "addr", addr, "version", version.String(),
		"db", dbPath, "recoveredTasks", recovered, "discovery", dpath)

	select {
	case <-ctx.Done():
	case err := <-serveErr:
		return err
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		log.Warn("http shutdown", "error", err)
	}
	log.Info("daemon stopped")
	return nil
}

func newLogger(level string) *slog.Logger {
	var l slog.Level
	switch strings.ToLower(level) {
	case "debug":
		l = slog.LevelDebug
	case "warn":
		l = slog.LevelWarn
	case "error":
		l = slog.LevelError
	default:
		l = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: l}))
}

func isLoopbackListen(listen string) bool {
	host, _, err := net.SplitHostPort(listen)
	if err != nil {
		return true
	}
	return host == "" || host == "127.0.0.1" || host == "localhost" || host == "::1"
}
