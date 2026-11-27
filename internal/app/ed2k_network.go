package app

// eD2k server list / KAD routing table adapter for the Web console. GETs are
// offline unless the engine is already running; explicit actions start it.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/asdfgh129/gobitmule/internal/api"
	"github.com/asdfgh129/gobitmule/internal/core"
	goed2k "github.com/asdfgh129/gobitmule/internal/ed2k"
	serverproto "github.com/asdfgh129/gobitmule/internal/ed2k/protocol/server"
)

func (a *ed2kSearchAdapter) requireEnabled() error {
	if !a.settings().Enabled {
		return core.Err(core.E_CAPABILITY_UNAVAILABLE,
			"eD2k 引擎未启用，请先在设置 → eMule 中开启并保存")
	}
	return nil
}

func metEntries(path string) ([]serverproto.ServerMetEntry, error) {
	if strings.TrimSpace(path) == "" {
		return nil, nil
	}
	if strings.Contains(path, "://") {
		return nil, fmt.Errorf("server.met 是远程地址，页面仅可编辑本地文件：%s", path)
	}
	met, err := serverproto.LoadServerMet(path)
	if err != nil {
		return nil, err
	}
	return met.Servers, nil
}

func (a *ed2kSearchAdapter) Servers(ctx context.Context) (api.ED2KServersStatus, error) {
	if err := ctx.Err(); err != nil {
		return api.ED2KServersStatus{}, err
	}
	cfg := a.settings()
	status := api.ED2KServersStatus{
		Available: cfg.Enabled, ServerMet: cfg.ServerMet,
		Servers: []api.ED2KServerEntry{},
	}
	if !cfg.Enabled {
		status.Message = "eD2k 引擎未启用，请先在设置 → eMule 中开启"
		return status, nil
	}

	c := a.liveClient()
	active := goed2k.ActiveNetworkStatus()
	runtimeServers := active.Servers
	if active.HasEngine && len(runtimeServers) > 0 {
		// Prefer the per-task download engine: it owns the configured incoming
		// TCP port, so its clientID is the one that matters for HighID.
		status.EngineStarted = true
		for _, info := range runtimeServers {
			if info.Connected {
				status.Connected = info.Identifier
				break
			}
		}
	} else if c != nil {
		status.EngineStarted = true
		status.Connected = c.Session().ConnectedServerID()
		runtimeServers = c.Session().ServerSnapshot()
	}

	entries, err := metEntries(cfg.ServerMet)
	if err != nil {
		status.Message = fmt.Sprintf("读取 server.met 失败：%v", err)
	}
	rows := make([]api.ED2KServerEntry, 0, len(entries))
	for i := range entries {
		entry := &entries[i]
		row := api.ED2KServerEntry{
			Address: entry.Address(), Name: entry.Name(),
			Description: entry.Description(), Source: "server.met",
		}
		if row.Address == "" {
			continue
		}
		rows = append(rows, row)
	}
	status.Servers = enrichServerRows(rows, runtimeServers)
	return status, nil
}

// enrichServerRows overlays live connection state onto the server.met rows by
// address. It uses slice indices rather than pointers into loop-local values:
// taking &row and appending row by value used to mutate a disconnected copy,
// leaving connected/handshakeComplete/clientId at zero in the API response.
func enrichServerRows(rows []api.ED2KServerEntry, runtime []goed2k.ServerConnectionInfo) []api.ED2KServerEntry {
	if len(runtime) == 0 {
		return rows
	}
	byAddress := make(map[string]int, len(rows))
	for i := range rows {
		byAddress[rows[i].Address] = i
	}
	for _, info := range runtime {
		if idx, ok := byAddress[info.Address]; ok {
			rows[idx].Connected = info.Connected
			rows[idx].HandshakeComplete = info.HandshakeComplete
			rows[idx].ClientID = info.ClientID
			rows[idx].LastMessage = info.LastMessage
			rows[idx].UsersCount = info.UsersCount
			rows[idx].FilesCount = info.FilesCount
			rows[idx].UDPFlags = info.UDPFlags
			if info.Name != "" {
				rows[idx].Name = info.Name
			}
			continue
		}
		rows = append(rows, api.ED2KServerEntry{
			Address: info.Address, Name: info.Identifier, Source: "运行时",
			Connected: info.Connected, HandshakeComplete: info.HandshakeComplete,
			ClientID: info.ClientID, LastMessage: info.LastMessage,
			UsersCount: info.UsersCount, FilesCount: info.FilesCount,
			UDPFlags: info.UDPFlags,
		})
	}
	return rows
}

func normalizeServerHostPort(address string) (string, int, error) {
	host, portText, err := net.SplitHostPort(strings.TrimSpace(address))
	if err != nil || strings.TrimSpace(host) == "" {
		return "", 0, fmt.Errorf("服务器地址格式应为 host:port：%q", address)
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return "", 0, fmt.Errorf("服务器端口无效：%q", portText)
	}
	return host, port, nil
}

func (a *ed2kSearchAdapter) AddServer(ctx context.Context, in api.ED2KServerInput) (api.ED2KServersStatus, error) {
	if err := ctx.Err(); err != nil {
		return api.ED2KServersStatus{}, err
	}
	if err := a.requireEnabled(); err != nil {
		return api.ED2KServersStatus{}, err
	}
	cfg := a.settings()
	if strings.TrimSpace(cfg.ServerMet) == "" {
		return api.ED2KServersStatus{}, core.Err(core.E_INVALID_INPUT,
			"请先在设置 → eMule 中配置 serverMet 文件路径")
	}
	host, port, err := normalizeServerHostPort(in.Address)
	if err != nil {
		return api.ED2KServersStatus{}, core.Err(core.E_INVALID_INPUT, "%v", err)
	}
	met := serverproto.NewServerMet()
	if existing, err := serverproto.LoadServerMet(cfg.ServerMet); err == nil {
		met = *existing
	} else if !errors.Is(err, os.ErrNotExist) {
		return api.ED2KServersStatus{}, core.Err(core.E_INTERNAL, "读取 server.met 失败：%v", err)
	}
	address := net.JoinHostPort(host, strconv.Itoa(port))
	for _, entry := range met.Servers {
		if entry.Address() == address || (entry.Host() == host && entry.Port() == port) {
			return a.Servers(ctx)
		}
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		name = host
	}
	entry, err := serverproto.NewServerMetEntryFromHost(host, port, name, in.Description)
	if err != nil {
		return api.ED2KServersStatus{}, core.Err(core.E_INVALID_INPUT, "添加服务器失败：%v", err)
	}
	met.AddServer(entry)
	if err := writeServerMet(cfg.ServerMet, &met); err != nil {
		return api.ED2KServersStatus{}, core.Err(core.E_INTERNAL, "写入 server.met 失败：%v", err)
	}
	return a.Servers(ctx)
}

func (a *ed2kSearchAdapter) RemoveServer(ctx context.Context, address string) (api.ED2KServersStatus, error) {
	if err := ctx.Err(); err != nil {
		return api.ED2KServersStatus{}, err
	}
	if err := a.requireEnabled(); err != nil {
		return api.ED2KServersStatus{}, err
	}
	cfg := a.settings()
	if strings.TrimSpace(cfg.ServerMet) == "" {
		return api.ED2KServersStatus{}, core.Err(core.E_INVALID_INPUT, "未配置 serverMet 文件路径")
	}
	host, port, err := normalizeServerHostPort(address)
	if err != nil {
		return api.ED2KServersStatus{}, core.Err(core.E_INVALID_INPUT, "%v", err)
	}
	entries, err := metEntries(cfg.ServerMet)
	if err != nil {
		return api.ED2KServersStatus{}, core.Err(core.E_INTERNAL, "读取 server.met 失败：%v", err)
	}
	met := serverproto.NewServerMet()
	removed := false
	for _, entry := range entries {
		if entry.Address() == address || (entry.Host() == host && entry.Port() == port) {
			removed = true
			continue
		}
		met.Servers = append(met.Servers, entry)
	}
	if !removed {
		return a.Servers(ctx)
	}
	if err := writeServerMet(cfg.ServerMet, &met); err != nil {
		return api.ED2KServersStatus{}, core.Err(core.E_INTERNAL, "写入 server.met 失败：%v", err)
	}
	return a.Servers(ctx)
}

func writeServerMet(path string, met *serverproto.ServerMet) error {
	if strings.Contains(path, "://") {
		return fmt.Errorf("server.met 是远程地址，页面仅可编辑本地文件：%s", path)
	}
	var buf bytes.Buffer
	if err := met.Put(&buf); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".servermet-*.dat")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(buf.Bytes()); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func (a *ed2kSearchAdapter) ConnectServer(ctx context.Context, address string) (api.ED2KServersStatus, error) {
	if err := ctx.Err(); err != nil {
		return api.ED2KServersStatus{}, err
	}
	if _, _, err := normalizeServerHostPort(address); err != nil {
		return api.ED2KServersStatus{}, core.Err(core.E_INVALID_INPUT, "%v", err)
	}
	c, err := a.ensureClient()
	if err != nil {
		return api.ED2KServersStatus{}, err
	}
	if err := c.ConnectServers(address); err != nil {
		return api.ED2KServersStatus{}, core.Err(core.E_TRANSIENT_NETWORK, "连接 eD2k 服务器失败：%v", err)
	}
	return a.Servers(ctx)
}

func (a *ed2kSearchAdapter) DisconnectServers(ctx context.Context) (api.ED2KServersStatus, error) {
	if err := ctx.Err(); err != nil {
		return api.ED2KServersStatus{}, err
	}
	if err := a.requireEnabled(); err != nil {
		return api.ED2KServersStatus{}, err
	}
	if c := a.liveClient(); c != nil {
		c.Session().DisconnectFrom()
	}
	return a.Servers(ctx)
}

func (a *ed2kSearchAdapter) Kad(ctx context.Context) (api.ED2KKadStatus, error) {
	if err := ctx.Err(); err != nil {
		return api.ED2KKadStatus{}, err
	}
	cfg := a.settings()
	status := api.ED2KKadStatus{Available: cfg.Enabled, Nodes: []api.ED2KKadNode{}}
	if !cfg.Enabled {
		status.Message = "eD2k 引擎未启用，请先在设置 → eMule 中开启"
		return status, nil
	}
	active := goed2k.ActiveNetworkStatus()
	var st goed2k.DHTStatus
	var state *goed2k.ClientDHTState
	if active.KadStatus != nil {
		// The per-task engine owns the configured UDP port; its firewalled flag
		// is the eMule-style green/orange indicator users need to see.
		status.EngineStarted = true
		st = *active.KadStatus
		state = active.KadState
	} else {
		c, err := a.ensureClient()
		if err != nil {
			return api.ED2KKadStatus{}, err
		}
		status.EngineStarted = true
		tracker := c.GetDHTTracker()
		if tracker == nil {
			status.Message = "KAD 尚未初始化，请在设置 → eMule 中配置 nodesDat 后保存"
			return status, nil
		}
		st = tracker.Status()
		state = tracker.SnapshotState()
	}
	status.Bootstrapped = st.Bootstrapped
	status.Firewalled = st.Firewalled
	status.LiveNodes = st.LiveNodes
	status.ReplacementNodes = st.ReplacementNodes
	status.RouterNodes = st.RouterNodes
	status.RunningTraversals = st.RunningTraversals
	status.KnownNodes = st.KnownNodes
	status.VerifiedNodes = st.VerifiedNodes
	status.ListenPort = st.ListenPort
	status.StoragePoint = st.StoragePoint

	if state != nil {
		status.SelfID = state.SelfID.String()
		routers := make(map[string]bool, len(state.RouterNodes))
		for _, addr := range state.RouterNodes {
			routers[addr] = true
		}
		for _, n := range state.Nodes {
			status.Nodes = append(status.Nodes, api.ED2KKadNode{
				ID: n.ID.String(), Address: n.Addr, TCPPort: int(n.TCPPort),
				Version: int(n.Version), Seed: n.Seed, HelloSent: n.HelloSent,
				Pinged: n.Pinged, Responded: n.Responded, FailCount: n.FailCount,
				FirstSeen: n.FirstSeen, LastSeen: n.LastSeen,
				Router: routers[n.Addr],
			})
		}
	}
	sort.Slice(status.Nodes, func(i, j int) bool {
		return status.Nodes[i].LastSeen > status.Nodes[j].LastSeen
	})
	if status.Message == "" && status.KnownNodes > 0 && status.VerifiedNodes == 0 {
		status.Message = fmt.Sprintf(
			"已加载 %d 个 KAD 节点，但尚未收到任何 UDP 应答；KAD 搜索可能无结果。请检查 UDP 端口与 KAD2 协议兼容性。",
			status.KnownNodes)
	}
	return status, nil
}

func (a *ed2kSearchAdapter) AddKadNode(ctx context.Context, address string) (api.ED2KKadStatus, error) {
	if err := ctx.Err(); err != nil {
		return api.ED2KKadStatus{}, err
	}
	if _, _, err := net.SplitHostPort(strings.TrimSpace(address)); err != nil {
		return api.ED2KKadStatus{}, core.Err(core.E_INVALID_INPUT, "KAD 节点地址格式应为 host:port")
	}
	c, err := a.ensureClient()
	if err != nil {
		return api.ED2KKadStatus{}, err
	}
	if err := c.AddDHTBootstrapNodes(address); err != nil {
		return api.ED2KKadStatus{}, core.Err(core.E_INVALID_INPUT, "添加 KAD 节点失败：%v", err)
	}
	return a.Kad(ctx)
}

// UpdateServersFromPeers implements the Web console action equivalent to
// eMule's "Update server list when a client connects". Connected clients send
// their current server in the tail of HelloAnswer; the engine records those
// addresses and this action folds them into server.met (deduplicated by
// AddServer, invalid/local addresses filtered at collection time).
func (a *ed2kSearchAdapter) UpdateServersFromPeers(ctx context.Context) (api.ED2KServersStatus, error) {
	if err := ctx.Err(); err != nil {
		return api.ED2KServersStatus{}, err
	}
	if err := a.requireEnabled(); err != nil {
		return api.ED2KServersStatus{}, err
	}
	addresses := goed2k.DiscoveredServers()
	before, err := a.Servers(ctx)
	if err != nil {
		return api.ED2KServersStatus{}, err
	}
	for _, address := range addresses {
		if err := ctx.Err(); err != nil {
			return api.ED2KServersStatus{}, err
		}
		if _, err := a.AddServer(ctx, api.ED2KServerInput{Address: address}); err != nil {
			// A single malformed/stale endpoint must not abort the whole update.
			continue
		}
	}
	status, err := a.Servers(ctx)
	if err != nil {
		return api.ED2KServersStatus{}, err
	}
	added := len(status.Servers) - len(before.Servers)
	if added < 0 {
		added = 0
	}
	status.Message = fmt.Sprintf("从已连接节点发现 %d 个服务器地址，新增 %d 个", len(addresses), added)
	return status, nil
}

// BootstrapKadFromPeers probes the Kad UDP ports learned from connected
// clients (eMule's Bootstrap action on a client in the peer/client list).
func (a *ed2kSearchAdapter) BootstrapKadFromPeers(ctx context.Context) (api.ED2KKadStatus, error) {
	if err := ctx.Err(); err != nil {
		return api.ED2KKadStatus{}, err
	}
	c, err := a.ensureClient()
	if err != nil {
		return api.ED2KKadStatus{}, err
	}
	nodes := goed2k.DiscoveredKadNodes()
	status, err := a.Kad(ctx)
	if err != nil {
		return api.ED2KKadStatus{}, err
	}
	if len(nodes) == 0 {
		status.Message = "尚未从已连接节点学到 Kad 地址，等握手完成后再试"
		return status, nil
	}
	if err := c.AddDHTBootstrapNodes(nodes...); err != nil {
		return api.ED2KKadStatus{}, core.Err(core.E_INVALID_INPUT, "从节点引导 KAD 失败：%v", err)
	}
	status, err = a.Kad(ctx)
	if err != nil {
		return api.ED2KKadStatus{}, err
	}
	status.Message = fmt.Sprintf("已从 %d 个已连接节点引导 KAD", len(nodes))
	return status, nil
}

// TaskSources exposes the live peer list of eD2k download tasks. It reads the
// registered per-task engines (not the search client), so the Web console can
// show eMule-style source state/queue/speed columns while a task is running.
func (a *ed2kSearchAdapter) TaskSources(ctx context.Context, hash string) ([]core.TaskSource, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out := make([]core.TaskSource, 0)
	for _, snap := range goed2k.ActivePeerStatuses() {
		if !strings.EqualFold(snap.TransferHash.String(), hash) {
			continue
		}
		p := snap.Peer
		// StrModVersion is the decoded CT_EMULE_VERSION ("v0.70a"). Printing the
		// raw packed integer as a fallback was worse than nothing: "0x11880" in a
		// table cell reads as a bug, so an undecodable version stays empty.
		version := strings.TrimSpace(p.StrModVersion)
		out = append(out, core.TaskSource{
			Endpoint:        p.Endpoint.String(),
			UserHash:        p.UserHash.String(),
			Client:          p.ModName,
			Version:         version,
			Sources:         p.SourceLabels(),
			State:           p.State,
			QueueRank:       p.QueueRank,
			RemoteQueueRank: p.RemoteQueueRank,
			Progress:        p.Progress,
			ProgressKnown:   p.ProgressKnown,
			PieceMap:        p.RemotePieces.HexString(),
			Pieces:          p.RemotePieces.Len(),
			// DownloadSpeed is payload+protocol, which is the only figure the
			// engine can report honestly: inbound sockets are charged in bulk by
			// Connection.DoRead before framing, so the payload channel stays 0 on
			// the receive side. Reporting PayloadDownloadSpeed here made every
			// source read 0 B/s while bytes were plainly arriving.
			DownloadSpeed: int64(p.DownloadSpeed),
			UploadSpeed:   int64(p.PayloadUploadSpeed),
			FailCount:     p.FailCount,
			FileName:      snap.FileName,
			RetryIn:       p.RetryIn,
			DeadStrikes:   p.DeadStrikes,
		})
	}
	return out, nil
}
