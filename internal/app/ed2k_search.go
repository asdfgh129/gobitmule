package app

import (
	"context"
	"log/slog"
	"strings"
	"sync"

	"github.com/asdfgh129/gobitmule/internal/api"
	"github.com/asdfgh129/gobitmule/internal/config"
	"github.com/asdfgh129/gobitmule/internal/core"
	goed2k "github.com/asdfgh129/gobitmule/internal/ed2k"
)

// ed2kSearchAdapter bridges the daemon's REST surface to the forked goed2k
// engine. The engine client is created lazily on the first search so startup
// stays offline; it is reused for subsequent searches and closed with daemon.
type ed2kSearchAdapter struct {
	cfg *config.Manager
	log *slog.Logger

	mu          sync.Mutex
	client      *goed2k.Client
	cfgSnapshot config.ED2KConfig
	// last keeps a stopped search visible in the UI: the engine drops its
	// active task on Stop, while users still expect the final state/results.
	last api.ED2KSearchStatus
}

func newED2KSearchAdapter(cfg *config.Manager, log *slog.Logger) *ed2kSearchAdapter {
	return &ed2kSearchAdapter{cfg: cfg, log: log, cfgSnapshot: cfg.Get().ED2K}
}

func (a *ed2kSearchAdapter) settings() config.ED2KConfig {
	return a.cfg.Get().ED2K
}

// ensureClient starts one dedicated search client. Its ports are offset from
// the downloader's configured ports: the downloader creates an engine per task
// (ADR-0002 single rate/state authority) and would otherwise fight for the same
// listener while a search is in flight.
func (a *ed2kSearchAdapter) ensureClient() (*goed2k.Client, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	cfg := a.cfg.Get().ED2K
	if !cfg.Enabled {
		return nil, core.Err(core.E_CAPABILITY_UNAVAILABLE,
			"eD2k 引擎未启用，请先在设置 → eMule 中开启并保存")
	}
	if a.client != nil {
		if a.cfgSnapshot == cfg {
			return a.client, nil
		}
		// Bootstrap files / ports changed from the settings page: restart the
		// dedicated client so the new values take effect without a daemon restart.
		a.log.Info("eD2k search: settings changed, restarting engine")
		if err := a.client.Stop(); err != nil {
			a.log.Warn("eD2k search: old engine stop", "error", err)
		}
		a.client = nil
	}

	st := goed2k.NewSettings()
	st.ListenPort = dedicatedPort(cfg.TCPPort)
	st.UDPPort = dedicatedPort(cfg.UDPPort)
	st.EnableDHT = cfg.NodesDat != ""
	st.SecureIdent = cfg.SecureIdent
	st.EnableObfuscation = cfg.Obfuscation
	// Route the engine's internal logx output to the daemon logger so KAD
	// traversal failures are visible at --log-level debug instead of vanishing.
	st.Logger = a.log
	c := goed2k.NewClient(st)
	c.SetAutoSaveInterval(0)

	// Bootstrap failures degrade the search scope, not the whole capability:
	// a server search can still work when nodes.dat is stale, and vice versa.
	if cfg.NodesDat != "" {
		if err := c.LoadDHTNodesDat(cfg.NodesDat); err != nil {
			a.log.Warn("eD2k search: KAD bootstrap unavailable", "error", err)
		}
	}
	if cfg.ServerMet != "" {
		if err := c.ConnectServerMet(cfg.ServerMet); err != nil {
			a.log.Warn("eD2k search: server bootstrap unavailable", "error", err)
		}
	}
	if err := c.Start(); err != nil {
		_ = c.Stop()
		return nil, core.Err(core.E_CAPABILITY_UNAVAILABLE, "eD2k search engine failed to start: %v", err)
	}
	a.client = c
	a.cfgSnapshot = cfg
	return c, nil
}

// liveClient returns the engine only when it has already been started, so GET
// endpoints stay offline (no listeners, no sockets) until a user acts.
func (a *ed2kSearchAdapter) liveClient() *goed2k.Client {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.client
}

func dedicatedPort(port int) int {
	if port > 0 && port < 65535 {
		return port + 1
	}
	return 0
}

func (a *ed2kSearchAdapter) Start(ctx context.Context, params api.ED2KSearchParams) (api.ED2KSearchStatus, error) {
	if err := ctx.Err(); err != nil {
		return api.ED2KSearchStatus{}, err
	}
	c, err := a.ensureClient()
	if err != nil {
		return api.ED2KSearchStatus{}, err
	}
	_, err = c.StartSearch(goed2k.SearchParams{
		Query:              strings.TrimSpace(params.Query),
		Scope:              toEngineScope(params.Scope),
		MinSize:            params.MinSize,
		MaxSize:            params.MaxSize,
		MinSources:         params.MinSources,
		MinCompleteSources: params.MinCompleteSources,
		FileType:           params.FileType,
		Extension:          params.Extension,
		FileTypes:          params.FileTypes,
		Sources:            params.Sources,
		Extensions:         params.Extensions,
	})
	if err != nil {
		if strings.Contains(err.Error(), "already in progress") {
			return api.ED2KSearchStatus{}, core.Err(core.E_INVALID_INPUT,
				"已有搜索正在进行，请先停止后再发起新搜索")
		}
		return api.ED2KSearchStatus{}, core.Err(core.E_INTERNAL, "start eD2k search: %v", err)
	}
	status := snapshotStatus(c.SearchSnapshot())
	a.setLast(status)
	return status, nil
}

func (a *ed2kSearchAdapter) Snapshot(ctx context.Context) (api.ED2KSearchStatus, error) {
	if err := ctx.Err(); err != nil {
		return api.ED2KSearchStatus{}, err
	}
	if !a.settings().Enabled {
		return unavailableStatus(), nil
	}
	c := a.liveClient()
	if c == nil {
		return emptyStatus(), nil
	}
	status := snapshotStatus(c.SearchSnapshot())
	if status.Snapshot.State == "" {
		// The engine has no active task (fresh engine or just stopped one).
		a.mu.Lock()
		last := a.last
		a.mu.Unlock()
		if last.Available {
			return last, nil
		}
		return emptyStatus(), nil
	}
	a.setLast(status)
	return status, nil
}

func (a *ed2kSearchAdapter) setLast(status api.ED2KSearchStatus) {
	a.mu.Lock()
	a.last = status
	a.mu.Unlock()
}

func (a *ed2kSearchAdapter) Stop(ctx context.Context) (api.ED2KSearchStatus, error) {
	if err := ctx.Err(); err != nil {
		return api.ED2KSearchStatus{}, err
	}
	if !a.settings().Enabled {
		return unavailableStatus(), nil
	}
	c := a.liveClient()
	if c == nil {
		return emptyStatus(), nil
	}
	before := snapshotStatus(c.SearchSnapshot())
	if before.Snapshot.State == "" {
		return emptyStatus(), nil
	}
	if err := c.StopSearch(); err != nil {
		return api.ED2KSearchStatus{}, core.Err(core.E_INTERNAL, "stop eD2k search: %v", err)
	}
	before.Snapshot.State = api.ED2KSearchStopped
	before.Snapshot.ServerBusy = false
	before.Snapshot.DHTBusy = false
	a.setLast(before)
	return before, nil
}

// Close releases the search engine's listeners. The daemon calls it after the
// HTTP server stops. Errors are logged: shutdown continues regardless.
func (a *ed2kSearchAdapter) Close() error {
	a.mu.Lock()
	c := a.client
	a.client = nil
	a.mu.Unlock()
	if c == nil {
		return nil
	}
	if err := c.Stop(); err != nil {
		a.log.Warn("eD2k search engine stop", "error", err)
	}
	return nil
}

func emptyStatus() api.ED2KSearchStatus {
	return api.ED2KSearchStatus{Available: true, Snapshot: api.ED2KSearchSnapshot{
		State: api.ED2KSearchIdle, Params: api.ED2KSearchParams{Scope: api.ED2KScopeAll},
		Results: []api.ED2KSearchResult{},
	}}
}

func unavailableStatus() api.ED2KSearchStatus {
	s := emptyStatus()
	s.Available = false
	return s
}

func toEngineScope(scope api.ED2KSearchScope) goed2k.SearchScope {
	switch scope {
	case api.ED2KScopeServer:
		return goed2k.SearchScopeServer
	case api.ED2KScopeKAD:
		return goed2k.SearchScopeDHT
	default:
		return goed2k.SearchScopeAll
	}
}

func snapshotStatus(s goed2k.SearchSnapshot) api.ED2KSearchStatus {
	results := make([]api.ED2KSearchResult, 0, len(s.Results))
	for _, r := range s.Results {
		results = append(results, api.ED2KSearchResult{
			Hash:            r.Hash.String(),
			FileName:        r.FileName,
			FileSize:        r.FileSize,
			Sources:         r.Sources,
			CompleteSources: r.CompleteSources,
			MediaBitrate:    r.MediaBitrate,
			MediaLength:     r.MediaLength,
			MediaCodec:      r.MediaCodec,
			Extension:       r.Extension,
			FileType:        r.FileType,
			Source:          toResultSource(r.Source),
			ED2KLink:        r.ED2KLink(),
		})
	}
	return api.ED2KSearchStatus{Available: true, Snapshot: api.ED2KSearchSnapshot{
		ID: s.ID,
		Params: api.ED2KSearchParams{
			Query:              s.Params.Query,
			Scope:              toAPIScope(s.Params.Scope),
			MinSize:            s.Params.MinSize,
			MaxSize:            s.Params.MaxSize,
			MinSources:         s.Params.MinSources,
			MinCompleteSources: s.Params.MinCompleteSources,
			FileType:           s.Params.FileType,
			Extension:          s.Params.Extension,
			FileTypes:          s.Params.FileTypes,
			Sources:            s.Params.Sources,
			Extensions:         s.Params.Extensions,
		},
		State:      api.ED2KSearchState(s.State),
		Results:    results,
		StartedAt:  s.StartedAt,
		UpdatedAt:  s.UpdatedAt,
		ServerBusy: s.ServerBusy,
		DHTBusy:    s.DHTBusy,
		KadKeyword: s.KadKeyword,
		Error:      s.Error,
	}}
}

func toAPIScope(scope goed2k.SearchScope) api.ED2KSearchScope {
	switch scope {
	case goed2k.SearchScopeServer:
		return api.ED2KScopeServer
	case goed2k.SearchScopeDHT:
		return api.ED2KScopeKAD
	default:
		return api.ED2KScopeAll
	}
}

func toResultSource(source goed2k.SearchResultSource) api.ED2KSearchResultSource {
	server := source&goed2k.SearchResultServer != 0
	kad := source&goed2k.SearchResultKAD != 0
	switch {
	case server && kad:
		return api.ED2KResultBoth
	case server:
		return api.ED2KResultServer
	case kad:
		return api.ED2KResultKAD
	default:
		return ""
	}
}
