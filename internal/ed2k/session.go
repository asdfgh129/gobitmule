package goed2k

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"

	"github.com/asdfgh129/gobitmule/internal/ed2k/disk"
	"github.com/asdfgh129/gobitmule/internal/ed2k/internal/logx"
	"github.com/asdfgh129/gobitmule/internal/ed2k/protocol"
	kadproto "github.com/asdfgh129/gobitmule/internal/ed2k/protocol/kad"
	serverproto "github.com/asdfgh129/gobitmule/internal/ed2k/protocol/server"
)

type Session struct {
	mu                     sync.Mutex
	diskMu                 sync.Mutex
	searchMu               sync.Mutex
	transfers              map[protocol.Hash]*Transfer
	connections            []*PeerConnection
	callbacks              map[int32]protocol.Hash
	settings               Settings
	lastTick               int64
	accumulator            Statistics
	serverConnection       *ServerConnection
	serverConnections      map[string]*ServerConnection
	serverConnectionPolicy map[string]*ServerConnectionPolicy
	configuredServers      map[string]*net.TCPAddr
	clientID               int32
	tcpFlags               int32
	auxPort                int32
	diskTasks              []*diskTask
	diskResults            chan diskTaskResult
	listener               *net.TCPListener
	incomingConns          chan net.Conn
	dhtTracker             *DHTTracker
	upnp                   *upnpManager
	uploadQueue            *UploadQueue
	credits                *PeerCreditManager
	friendSlots            map[string]bool
	activeSearch           *searchTask
	nextSearchID           uint32
	serverUDP              *ServerUDP // #2: server UDP socket (global search / glob sources)
	friendList             *FriendList // #6: friends and chat identities
	onChatMessage          func(userHash protocol.Hash, text string)
	ipFilter               *IPFilter // #10: eMule ipfilter.dat equivalent
	spamFilter             *SpamFilter // #11: search result rating/spam filter
	secureIdent            secureIdentIdentity // #5: long term RSA identity for Secure Ident
}

type diskTask struct {
	callable TransferCallable
	transfer *Transfer
	done     bool
	running  bool
}

type diskTaskResult struct {
	task   *diskTask
	result AsyncOperationResult
}

func NewSession(st Settings) *Session {
	session := &Session{
		transfers:              make(map[protocol.Hash]*Transfer),
		connections:            make([]*PeerConnection, 0),
		callbacks:              make(map[int32]protocol.Hash),
		settings:               st,
		lastTick:               CurrentTime(),
		accumulator:            NewStatistics(),
		serverConnections:      make(map[string]*ServerConnection),
		serverConnectionPolicy: make(map[string]*ServerConnectionPolicy),
		configuredServers:      make(map[string]*net.TCPAddr),
		diskTasks:              make([]*diskTask, 0),
		diskResults:            make(chan diskTaskResult, 128),
		incomingConns:          make(chan net.Conn, 32),
		credits:                NewPeerCreditManager(),
		friendSlots:            make(map[string]bool),
	}
	session.friendList = NewFriendList()
	session.ipFilter = NewIPFilter()
	session.spamFilter = NewSpamFilter()
	session.serverUDP = NewServerUDP()
	session.serverUDP.onResults = func(hash protocol.Hash, endpoints []protocol.Endpoint) {
		session.onUDPSources(hash, endpoints)
	}
	session.serverUDP.onSearch = func(entries []serverproto.SharedFileEntry) {
		session.onUDPSearchResults(entries)
	}
	session.initUploadQueue()
	return session
}

func (s *Session) initUploadQueue() {
	if s.uploadQueue == nil {
		s.uploadQueue = NewUploadQueue(s)
	}
}

func (s *Session) GetCurrentTime() int64 {
	return s.lastTick
}

func (s *Session) ConfigureSession(st Settings) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.settings = st
}

func (s *Session) AddTransfer(hash protocol.Hash, size int64, file *os.File) (TransferHandle, error) {
	s.mu.Lock()
	if t, ok := s.transfers[hash]; ok {
		s.mu.Unlock()
		return NewTransferHandleWithTransfer(s, t), nil
	}
	s.mu.Unlock()

	t, err := NewTransfer(s, NewAddTransferParamsFromFile(hash, CurrentTimeMillis(), size, file, false))
	if err != nil {
		return NewTransferHandle(s), err
	}
	s.mu.Lock()
	if existing, ok := s.transfers[hash]; ok {
		s.mu.Unlock()
		return NewTransferHandleWithTransfer(s, existing), nil
	}
	s.transfers[hash] = t
	s.mu.Unlock()
	return NewTransferHandleWithTransfer(s, t), nil
}

func (s *Session) AddTransferWithHandler(hash protocol.Hash, size int64, handler disk.FileHandler) (TransferHandle, error) {
	s.mu.Lock()
	if t, ok := s.transfers[hash]; ok {
		s.mu.Unlock()
		return NewTransferHandleWithTransfer(s, t), nil
	}
	s.mu.Unlock()

	t, err := NewTransfer(s, NewAddTransferParamsFromHandler(hash, CurrentTimeMillis(), size, handler, false))
	if err != nil {
		return NewTransferHandle(s), err
	}
	s.mu.Lock()
	if existing, ok := s.transfers[hash]; ok {
		s.mu.Unlock()
		return NewTransferHandleWithTransfer(s, existing), nil
	}
	s.transfers[hash] = t
	s.mu.Unlock()
	return NewTransferHandleWithTransfer(s, t), nil
}

func (s *Session) AddTransferParams(atp AddTransferParams) (TransferHandle, error) {
	s.mu.Lock()
	if t, ok := s.transfers[atp.Hash]; ok {
		s.mu.Unlock()
		return NewTransferHandleWithTransfer(s, t), nil
	}
	s.mu.Unlock()

	t, err := NewTransfer(s, atp)
	if err != nil {
		return NewTransferHandle(s), err
	}
	s.mu.Lock()
	if existing, ok := s.transfers[atp.Hash]; ok {
		s.mu.Unlock()
		return NewTransferHandleWithTransfer(s, existing), nil
	}
	s.transfers[atp.Hash] = t
	s.mu.Unlock()
	return NewTransferHandleWithTransfer(s, t), nil
}

func (s *Session) FindTransfer(hash protocol.Hash) TransferHandle {
	s.mu.Lock()
	defer s.mu.Unlock()
	return NewTransferHandleWithTransfer(s, s.transfers[hash])
}

func (s *Session) LookupTransfer(hash protocol.Hash) *Transfer {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.transfers[hash]
}

func (s *Session) RemoveTransfer(hash protocol.Hash, deleteFile bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	t := s.transfers[hash]
	if t == nil {
		return nil
	}
	if err := t.Abort(deleteFile); err != nil {
		return err
	}
	delete(s.transfers, hash)
	return nil
}

func (s *Session) GetTransfers() []TransferHandle {
	s.mu.Lock()
	defer s.mu.Unlock()

	handles := make([]TransferHandle, 0, len(s.transfers))
	for _, t := range s.transfers {
		handles = append(handles, NewTransferHandleWithTransfer(s, t))
	}
	return handles
}

func (s *Session) CloseConnection(connection *PeerConnection) {
	s.mu.Lock()
	defer s.mu.Unlock()

	dst := s.connections[:0]
	for _, c := range s.connections {
		if c != connection {
			dst = append(dst, c)
		}
	}
	s.connections = dst
}

func (s *Session) GetUserAgent() protocol.Hash {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.settings.UserAgent
}

func (s *Session) SetUserAgent(hash protocol.Hash) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.settings.UserAgent = hash
}

func (s *Session) GetSecureIdent() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.settings.SecureIdent
}

// GetEnableObfuscation reports whether incoming peer connections should
// accept eMule basic obfuscation. Plaintext peers are still accepted.
func (s *Session) GetEnableObfuscation() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.settings.EnableObfuscation
}

func (s *Session) SetSecureIdent(enabled bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.settings.SecureIdent = enabled
}

func (s *Session) GetClientID() int32 {
	return s.clientID
}

func (s *Session) GetListenPort() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.settings.ListenPort
}

// #1: server callback closes the Low-ID hole. A Low-ID peer cannot be dialed
// directly (its "IP" is a server token), so eMule asks the server to relay a
// callback request; the server replies with the real IP:port and we dial it.
// RequestCallbackForLowID records the pending request and sends OP_CALLBACKREQUEST.
func (s *Session) RequestCallbackForLowID(clientID int32, hash protocol.Hash) bool {
	if s == nil || clientID == 0 || !IsLowID(clientID) || hash.Equal(protocol.Invalid) {
		return false
	}
	sc := s.serverConnection
	if sc == nil || !sc.IsHandshakeCompleted() {
		return false
	}
	s.mu.Lock()
	if s.callbacks == nil {
		s.callbacks = make(map[int32]protocol.Hash)
	}
	s.callbacks[clientID] = hash
	s.mu.Unlock()
	sc.SendCallbackRequest(clientID)
	debugPeerf("server callback requested for low-id=%d file=%s", clientID, hash.String())
	return true
}

// HandleCallbackRequest consumes OP_CALLBACKREQUESTED: the server tells us the
// real IP:port of a Low-ID peer that wants to connect. The callback does not
// carry a file hash, so eMule hands the endpoint to its client list and lets
// every download that wants sources try it. We mirror that by offering the
// endpoint to all active transfers that need more peers.
func (s *Session) HandleCallbackRequest(point protocol.Endpoint) int {
	if s == nil || !point.Defined() || IsLowID(point.IP()) {
		return 0
	}
	s.mu.Lock()
	// A satisfied callback clears its pending entry (by matching IP as the
	// server may return a hybrid ID rather than the original token).
	for id := range s.callbacks {
		if id == point.IP() {
			delete(s.callbacks, id)
		}
	}
	transfers := make([]*Transfer, 0, len(s.transfers))
	for _, t := range s.transfers {
		if t != nil && !t.IsPaused() && !t.IsAborted() {
			transfers = append(transfers, t)
		}
	}
	s.mu.Unlock()
	added := 0
	for _, t := range transfers {
		if err := t.AddPeer(point, int(PeerServer)); err == nil {
			added++
		}
	}
	debugPeerf("server callback endpoint=%s offered to %d transfers", point.String(), added)
	return added
}

// #3 A4AF ("ask for another file"): when a connected peer reports it does not
// hold the file we requested, eMule reassigns that source to another download
// that still needs peers instead of discarding the connection. We mirror the
// useful subset: offer the endpoint to the other active transfers that need
// more sources, so a source shared across files is not wasted.
func (s *Session) A4AFReassign(endpoint protocol.Endpoint, fromHash protocol.Hash) int {
	if s == nil || !endpoint.Defined() || IsLowID(endpoint.IP()) {
		return 0
	}
	s.mu.Lock()
	targets := make([]*Transfer, 0, len(s.transfers))
	for hash, t := range s.transfers {
		if hash.Equal(fromHash) || t == nil || t.IsPaused() || t.IsAborted() || t.IsFinished() {
			continue
		}
		if !t.NeedMoreSources() {
			continue
		}
		targets = append(targets, t)
	}
	s.mu.Unlock()
	reassigned := 0
	for _, t := range targets {
		if err := t.AddPeer(endpoint, int(PeerServer)); err == nil {
			reassigned++
		}
	}
	if reassigned > 0 {
		debugPeerf("a4af source %s reassigned to %d transfers (from %s)",
			endpoint.String(), reassigned, fromHash.String())
	}
	return reassigned
}

// BootstrapKadFromPeer adds a Kad UDP contact learned from a connected peer,
// mirroring eMule's CKademlia::Bootstrap(peerIP, peerKadPort) on Hello.
func (s *Session) BootstrapKadFromPeer(address string) bool {
	if s == nil || s.dhtTracker == nil || address == "" {
		return false
	}
	addr, err := net.ResolveUDPAddr("udp", address)
	if err != nil || addr == nil {
		return false
	}
	s.dhtTracker.AddNode(addr)
	s.dhtTracker.ProbeNode(addr)
	return true
}

func (s *Session) GetUDPPort() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dhtTracker != nil {
		if port := s.dhtTracker.ListenPort(); port > 0 {
			return port
		}
	}
	return s.settings.UDPPort
}

func (s *Session) GetClientName() string {
	return s.settings.ClientName
}

func (s *Session) GetModName() string {
	return s.settings.ModName
}

func (s *Session) GetAppVersion() int {
	return s.settings.Version
}

func (s *Session) GetCompressionVersion() int {
	return s.settings.CompressionVersion
}

func (s *Session) GetModMajorVersion() int {
	return s.settings.ModMajor
}

func (s *Session) GetModMinorVersion() int {
	return s.settings.ModMinor
}

func (s *Session) GetModBuildVersion() int {
	return s.settings.ModBuild
}

func (s *Session) SendSourcesRequest(hash protocol.Hash, size int64) bool {
	sent := false
	for _, sc := range s.activeServerConnections() {
		if sc == nil || !sc.IsHandshakeCompleted() {
			continue
		}
		sc.SendFileSourcesRequest(hash, size)
		sent = true
	}
	return sent
}

func (s *Session) SendDHTSourcesRequest(hash protocol.Hash, size int64, transfer *Transfer) bool {
	if s.dhtTracker == nil || transfer == nil {
		return false
	}
	return s.dhtTracker.SearchSources(hash, size, func(results []kadproto.SearchEntry) {
		s.mu.Lock()
		current := s.transfers[hash]
		s.mu.Unlock()
		if current == nil || current != transfer {
			logx.Debug("dht source search result dropped", "hash", hash.String(),
				"results", len(results), "transfer_match", current == transfer)
			return
		}
		// ED-05: a KAD source result carries the source node's 16-byte ID,
		// which is the eD2k user hash anchor for outgoing obfuscation. Only
		// carry it when obfuscation is explicitly enabled; sources without
		// a usable hash keep the plaintext path.
		obfuscationEnabled := s.GetEnableObfuscation()
		parsed, added := 0, 0
		for idx, entry := range results {
			endpoint, ok := entry.SourceEndpoint()
			if !ok || !endpoint.Defined() {
				if idx < 3 {
					logx.Debug("dht source entry skipped", "hash", hash.String(),
						"endpoint_ok", ok, "endpoint", endpoint.String(), "tags", describeKadTags(entry.Tags))
				}
				continue
			}
			parsed++
			if idx < 5 {
				sourceType, _ := entry.UIntTag(kadproto.TagSourceType)
				logx.Debug("dht source entry accepted", "hash", hash.String(),
					"endpoint", endpoint.String(), "sourceType", sourceType,
					"nodeID", entry.ID.Hash.String(), "tags", describeKadTags(entry.Tags))
			}
			var err error
			if obfuscationEnabled && !entry.ID.Hash.Equal(protocol.Invalid) {
				err = current.AddPeerWithHash(endpoint, int(PeerDHT), entry.ID.Hash)
			} else {
				err = current.AddPeer(endpoint, int(PeerDHT))
			}
			if err == nil {
				added++
			}
		}
		logx.Debug("dht source search result", "hash", hash.String(),
			"results", len(results), "parsed", parsed, "added", added)
	})
}

// onUDPSources feeds OP_GLOBFOUNDSOURCES endpoints into the matching transfer.
func (s *Session) onUDPSources(hash protocol.Hash, endpoints []protocol.Endpoint) {
	if s == nil || len(endpoints) == 0 {
		return
	}
	s.mu.Lock()
	transfer := s.transfers[hash]
	s.mu.Unlock()
	if transfer == nil {
		return
	}
	added := 0
	for _, endpoint := range endpoints {
		if !endpoint.Defined() || IsLowID(endpoint.IP()) {
			continue
		}
		if err := transfer.AddPeer(endpoint, int(PeerServer)); err == nil {
			added++
		}
	}
	debugPeerf("server udp sources hash=%s got=%d added=%d", hash.String(), len(endpoints), added)
}

// onUDPSearchResults feeds OP_GLOBSEARCHRES entries into the active search.
func (s *Session) onUDPSearchResults(entries []serverproto.SharedFileEntry) {
	if s == nil || len(entries) == 0 {
		return
	}
	s.searchMu.Lock()
	task := s.activeSearch
	s.searchMu.Unlock()
	if task == nil || task.state != SearchStateRunning {
		return
	}
	params := task.params
	count := 0
	for _, entry := range entries {
		result := makeSearchResultFromServer(entry)
		if !matchesSearchFilters(result, params) {
			continue
		}
		if !s.applySearchRating(&result) {
			continue
		}
		task.mergeResult(result)
		count++
	}
	debugPeerf("server udp search results got=%d added=%d", len(entries), count)
}

// serverUDPAddr resolves the connected server's UDP endpoint. eMule uses
// TCP port + 4 for the server UDP socket.
func (s *Session) serverUDPAddr() *net.UDPAddr {
	if s == nil || s.serverConnection == nil {
		return nil
	}
	tcpAddr := s.serverConnection.GetAddress()
	if tcpAddr == nil {
		return nil
	}
	ip := tcpAddr.IP
	if ip == nil {
		return nil
	}
	return &net.UDPAddr{IP: ip, Port: tcpAddr.Port + 4}
}

// SendUDPSourcesRequest sends an OP_GLOBGETSOURCES2 datagram to the connected
// server. The opcode depends on the capabilities the server advertised in
// OP_SERVERIDENT: 0x94 carries <hash><size> when SRV_UDPFLG_EXT_GETSOURCES2 is
// set, the legacy 0x9A carries the bare hash. A server without
// SRV_UDPFLG_EXT_GETSOURCES cannot answer UDP source requests at all, matching
// CDownloadQueue::IsMaxFilesPerUDPServerPacketReached.
func (s *Session) SendUDPSourcesRequest(hash protocol.Hash, size int64) bool {
	if s == nil || s.serverUDP == nil {
		return false
	}
	addr := s.serverUDPAddr()
	if addr == nil {
		return false
	}
	flags := s.serverUDPFlags()
	var datagram []byte
	switch {
	case flags&serverproto.UDPFlagExtGetSources2 != 0:
		datagram = serverproto.UDPGlobGetSources(hash, size)
	case flags&serverproto.UDPFlagExtGetSources != 0:
		datagram = serverproto.UDPGlobGetSourcesLegacy(hash)
	default:
		debugPeerf("server udp: no glob-sources capability flags=0x%x", flags)
		return false
	}
	ok := s.serverUDP.SendTo(addr, datagram)
	debugPeerf("server udp -> glob sources %s to %s ok=%t flags=0x%x", hash.String(), addr.String(), ok, flags)
	return ok
}

// SetServerUDPKey installs the server's datagram obfuscation key. eMule only
// uses it when the server bound the key to our public IP (CServer::GetServerKeyUDP);
// a key issued for another address would make every datagram undecryptable.
func (s *Session) SetServerUDPKey(key, keyIP uint32) {
	if s == nil || s.serverUDP == nil || key == 0 {
		return
	}
	s.mu.Lock()
	clientID := s.clientID
	s.mu.Unlock()
	if keyIP != 0 && clientID != 0 && keyIP != uint32(clientID) {
		debugPeerf("server udp: ignoring key issued for %d (our id %d)", keyIP, clientID)
		return
	}
	s.serverUDP.SetUDPKey(key)
	debugPeerf("server udp: obfuscation key installed keyIP=%d", keyIP)
}

// serverUDPFlags returns the connected server's UDP capability flags.
func (s *Session) serverUDPFlags() uint32 {
	s.mu.Lock()
	sc := s.serverConnection
	s.mu.Unlock()
	if sc == nil {
		return 0
	}
	return sc.UDPFlags()
}

// SendUDPSearchRequest sends OP_GLOBSEARCHREQ3 to the connected server.
func (s *Session) SendUDPSearchRequest(req *serverproto.SearchRequest) bool {
	if s == nil || s.serverUDP == nil || req == nil {
		return false
	}
	addr := s.serverUDPAddr()
	if addr == nil {
		return false
	}
	datagram, err := serverproto.UDPGlobSearchReq(req)
	if err != nil {
		return false
	}
	ok := s.serverUDP.SendTo(addr, datagram)
	debugPeerf("server udp -> glob search %q to %s ok=%t", req.Query, addr.String(), ok)
	return ok
}

// #6: friend and chat management surfaced through the session.
// IPFilter returns the peer block list; it is never nil for a constructed
// session and starts empty (disabled).
func (s *Session) IPFilter() *IPFilter {
	if s.ipFilter == nil {
		s.ipFilter = NewIPFilter()
	}
	return s.ipFilter
}

// LoadIPFilter replaces the peer block list from an ipfilter.dat style file
// (#10). It returns the number of ranges loaded.
func (s *Session) LoadIPFilter(path string) (int, error) {
	return s.IPFilter().LoadFile(path)
}

// AddFriend registers a friend so chat messages from that user hash are
// accepted. It replaces the existing entry with the same hash.
func (s *Session) AddFriend(friend Friend) bool {
	if s == nil || s.friendList == nil {
		return false
	}
	return s.friendList.Add(friend)
}

func (s *Session) RemoveFriend(hash protocol.Hash) bool {
	if s == nil || s.friendList == nil {
		return false
	}
	return s.friendList.Remove(hash)
}

func (s *Session) Friends() []Friend {
	if s == nil || s.friendList == nil {
		return []Friend{}
	}
	return s.friendList.List()
}

func (s *Session) SetChatMessageHandler(fn func(protocol.Hash, string)) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.onChatMessage = fn
	s.mu.Unlock()
}

// SendChatMessage sends a message to the first connected peer with the given
// user hash. Returns false when the peer is not connected.
func (s *Session) SendChatMessage(hash protocol.Hash, text string) bool {
	if s == nil || text == "" {
		return false
	}
	s.mu.Lock()
	connections := append([]*PeerConnection(nil), s.connections...)
	s.mu.Unlock()
	for _, c := range connections {
		if c != nil && c.remoteHash.Equal(hash) {
			return c.SendMessage(text)
		}
	}
	return false
}

func describeKadTags(tags []kadproto.Tag) string {
	parts := make([]string, 0, len(tags))
	for _, tag := range tags {
		name := tag.Name
		if name == "" {
			name = fmt.Sprintf("id=%#x", tag.ID)
		}
		value := fmt.Sprintf("%#x/%d", tag.UInt64, tag.UInt64)
		if tag.String != "" {
			value = fmt.Sprintf("%q", tag.String)
		} else if len(tag.Bytes) > 0 {
			value = fmt.Sprintf("%x", tag.Bytes)
		}
		parts = append(parts, fmt.Sprintf("%s(t=%#x)=%s", name, tag.Type, value))
	}
	return strings.Join(parts, ",")
}

func (s *Session) RequestSourcesNow(transfer *Transfer) bool {
	if transfer == nil || transfer.IsPaused() || transfer.IsAborted() || transfer.IsFinished() {
		return false
	}
	transfer.ForceSourceDiscoveryNow()
	activePeers := transfer.ActiveConnections()
	knownPeers := transfer.policy.Size()
	serverSent := s.SendSourcesRequest(transfer.hash, transfer.size)
	// #2: the UDP glob source request is an independent path; a TCP failure
	// does not need to block it, and both may be answered by different servers.
	udpSent := s.SendUDPSourcesRequest(transfer.hash, transfer.size)
	sent := serverSent || udpSent
	dhtSent := s.SendDHTSourcesRequest(transfer.hash, transfer.size, transfer)
	if dhtSent {
		sent = true
	}
	now := CurrentTime()
	transfer.nextSourcesRequest = now + transfer.nextServerSourcesInterval(activePeers, knownPeers, serverSent)
	transfer.nextDHTRequest = now + transfer.nextDHTSourcesInterval(activePeers, knownPeers, dhtSent)
	logx.Debug("source discovery triggered", "hash", transfer.hash.String(), "server", serverSent, "dht", dhtSent, "active_peers", activePeers, "known_peers", knownPeers)
	return sent
}

func (s *Session) ConnectNewPeers() {
	stepsSinceLastConnect := 0
	maxConnectionsPerSecond := s.settings.MaxConnectionsPerSecond
	transfers := s.snapshotTransfers()
	connections := s.snapshotConnections()
	numTransfers := len(transfers)
	enumerateCandidates := true
	if numTransfers > 0 && len(connections) < s.settings.SessionConnectionsLimit {
		for enumerateCandidates {
			for _, t := range transfers {
				if t.WantMorePeers() {
					connected, _ := t.TryConnectPeer(CurrentTime())
					if connected {
						maxConnectionsPerSecond--
						stepsSinceLastConnect = 0
					}
				}
				stepsSinceLastConnect++
				if stepsSinceLastConnect > numTransfers*2 {
					enumerateCandidates = false
					break
				}
				if maxConnectionsPerSecond == 0 {
					enumerateCandidates = false
					break
				}
			}
			if len(transfers) == 0 {
				break
			}
		}
	}
}

func (s *Session) Listen() error {
	if s.listener != nil {
		return nil
	}
	s.mu.Lock()
	port := s.settings.ListenPort
	s.mu.Unlock()
	if port < 0 {
		return nil
	}
	listener, err := net.ListenTCP("tcp", &net.TCPAddr{Port: port})
	if err != nil && port > 0 {
		// A dedicated search engine and one or more download engines can run
		// in the same daemon on the configured port. Falling back to an
		// ephemeral port keeps every engine usable; eD2k peers are reached by
		// outbound connections, so a fixed inbound port is not required.
		logx.Debug("listen port busy, falling back to ephemeral", "port", port, "err", err)
		listener, err = net.ListenTCP("tcp", &net.TCPAddr{Port: 0})
	}
	if err != nil {
		return err
	}
	if addr, ok := listener.Addr().(*net.TCPAddr); ok {
		s.mu.Lock()
		s.settings.ListenPort = addr.Port
		s.mu.Unlock()
	}
	s.listener = listener
	// #2: server UDP datagrams share the configured eD2k UDP port, which is the
	// source port peers see (eMule binds CClientUDPSocket on UDPPort). The
	// destination is always a server's TCP port + 4. A failure to bind is
	// non-fatal: TCP search/source discovery still works.
	if s.serverUDP != nil {
		s.mu.Lock()
		udpPort := s.settings.UDPPort
		if udpPort == 0 {
			udpPort = s.settings.ListenPort + 4
		}
		s.mu.Unlock()
		if udpPort >= 0 && udpPort <= 65535 {
			_ = s.serverUDP.Start(udpPort)
		}
	}
	go func(l *net.TCPListener) {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			select {
			case s.incomingConns <- conn:
			default:
				_ = conn.Close()
			}
		}
	}(listener)
	s.startUPnPMapping()
	return nil
}

func (s *Session) CloseListener() {
	s.stopUPnPMapping()
	if s.serverUDP != nil {
		s.serverUDP.Close()
	}
	if s.listener == nil {
		return
	}
	_ = s.listener.Close()
	s.listener = nil
}

func (s *Session) SyncDHTListenPort() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dhtTracker == nil {
		return
	}
	if port := s.dhtTracker.ListenPort(); port > 0 {
		s.settings.UDPPort = port
	}
}

func (s *Session) RefreshUPnPMapping() {
	s.stopUPnPMapping()
	s.startUPnPMapping()
}

func (s *Session) SecondTick(currentSessionTime, tickIntervalMS int64) {
	s.PumpIO()
	for _, t := range s.snapshotTransfers() {
		t.SecondTick(&s.accumulator, tickIntervalMS)
	}
	for _, sc := range s.activeServerConnections() {
		if sc != nil {
			sc.SecondTick(tickIntervalMS)
		}
	}
	if s.settings.ReconnectToServer {
		s.connectConfiguredServers(currentSessionTime)
	}
	if s.uploadQueue != nil {
		s.uploadQueue.Process()
	}
	s.tickSearches(currentSessionTime)
	s.processDiskTasks()
	s.accumulator.SecondTick(tickIntervalMS)
	s.ConnectNewPeers()
}

func (s *Session) PumpIO() {
	s.acceptIncomingConnections()
	for _, sc := range s.activeServerConnections() {
		if sc == nil {
			continue
		}
		if sc.IsDisconnecting() {
			if !sc.IsDisconnectHandled() {
				sc.OnDisconnect(sc.DisconnectCode())
				sc.MarkDisconnectHandled()
			}
			continue
		}
		if sc.pumpDial() {
			// The server dial is still in flight; there is no socket to serve.
			continue
		}
		if err := sc.FlushOutgoing(); err != nil {
			debugPeerf("server %s flush error: %v", sc.GetIdentifier(), err)
		}
		if err := sc.DoRead(); err != nil {
			debugPeerf("server %s read error: %v", sc.GetIdentifier(), err)
		}
		if err := sc.ProcessIncoming(); err != nil {
			debugPeerf("server %s process error: %v", sc.GetIdentifier(), err)
		}
		if sc.IsDisconnecting() && !sc.IsDisconnectHandled() {
			sc.OnDisconnect(sc.DisconnectCode())
			sc.MarkDisconnectHandled()
		}
	}
	for _, conn := range s.snapshotConnections() {
		if conn == nil {
			continue
		}
		if conn.IsDisconnecting() {
			if !conn.IsDisconnectHandled() {
				conn.OnDisconnect(conn.DisconnectCode())
				conn.MarkDisconnectHandled()
			}
			continue
		}
		if conn.pumpDial() {
			// A background dial is still running: there is no socket to serve
			// yet, and blocking here is exactly what used to starve PumpIO.
			continue
		}
		if err := conn.FlushOutgoing(); err != nil {
			debugPeerf("peer %s flush error: %v", conn.Endpoint().String(), err)
		}
		if err := conn.DoRead(); err != nil {
			debugPeerf("peer %s read error: %v", conn.Endpoint().String(), err)
		}
		if conn.transferringData {
			conn.ReceivePendingData()
		} else {
			if err := conn.ProcessIncoming(); err != nil {
				debugPeerf("peer %s process error: %v", conn.Endpoint().String(), err)
			}
		}
		if conn.IsDisconnecting() && !conn.IsDisconnectHandled() {
			conn.OnDisconnect(conn.DisconnectCode())
			conn.MarkDisconnectHandled()
		}
	}
}

func (s *Session) acceptIncomingConnections() {
	for {
		select {
		case conn := <-s.incomingConns:
			if conn == nil {
				continue
			}
			if s.IPFilter().IsFilteredAddr(conn.RemoteAddr()) {
				debugPeerf("incoming %s blocked by ipfilter", conn.RemoteAddr().String())
				_ = conn.Close()
				continue
			}
			pc := NewIncomingPeerConnection(s, conn)
			// ED-05: accept eMule basic obfuscation on incoming peer sockets.
			// The short probe first distinguishes cleartext peers, real
			// obfuscated peers and the silent eD2k server port callback. The
			// callback expects us to send the first client Hello; answering it
			// is what turns a forwarded 4662 port into a HighID.
			greetingNeeded, err := pc.prepareIncoming(s.GetUserAgent(), s.GetEnableObfuscation())
			if err != nil {
				debugPeerf("incoming handshake failed: %v", err)
				pc.Close(EncryptionError)
				continue
			}
			s.mu.Lock()
			s.connections = append(s.connections, pc)
			s.mu.Unlock()
			if greetingNeeded {
				debugPeerf("incoming %s is a silent probe, sending Hello", conn.RemoteAddr().String())
				pc.OnConnect()
			}
		default:
			return
		}
	}
}

func (s *Session) ConnectTo(identifier string, address *net.TCPAddr) error {
	if identifier == "" || address == nil {
		return NewError(InternalError)
	}
	s.mu.Lock()
	s.configuredServers[identifier] = address
	policy := s.ensureServerConnectionPolicy(identifier)
	policy.SetConnectCandidate(identifier, address, CurrentTime())
	if existing := s.serverConnections[identifier]; existing != nil {
		if existing.GetAddress() != nil && existing.GetAddress().String() == address.String() && !existing.IsDisconnecting() {
			s.promotePrimaryServer(existing)
			s.mu.Unlock()
			return nil
		}
		existing.Close(NoError)
	}
	sc := NewServerConnection(identifier, address, s)
	s.serverConnections[identifier] = sc
	s.promotePrimaryServer(sc)
	s.mu.Unlock()
	return sc.Connect()
}

func (s *Session) DisconnectFrom() {
	s.mu.Lock()
	for identifier, policy := range s.serverConnectionPolicy {
		if policy != nil {
			policy.RemoveConnectCandidates()
		}
		delete(s.serverConnectionPolicy, identifier)
	}
	for identifier, sc := range s.serverConnections {
		if sc != nil {
			sc.Close(NoError)
		}
		delete(s.serverConnections, identifier)
	}
	for identifier := range s.configuredServers {
		delete(s.configuredServers, identifier)
	}
	s.serverConnection = nil
	s.clientID = 0
	s.tcpFlags = 0
	s.auxPort = 0
	s.mu.Unlock()
}

func (s *Session) ConnectedServerID() string {
	if s.serverConnection != nil && s.serverConnection.IsHandshakeCompleted() {
		return s.serverConnection.GetIdentifier()
	}
	return ""
}

func (s *Session) ConnectedServerIDs() []string {
	ids := make([]string, 0, len(s.serverConnections))
	for identifier, sc := range s.serverConnections {
		if sc != nil && sc.IsHandshakeCompleted() {
			ids = append(ids, identifier)
		}
	}
	return ids
}

func (s *Session) OnServerConnectionClosed(sc *ServerConnection, ec BaseErrorCode) {
	s.mu.Lock()
	if sc != nil {
		delete(s.serverConnections, sc.GetIdentifier())
		if s.settings.ReconnectToServer && ec.Code() != NoError.Code() {
			policy := s.ensureServerConnectionPolicy(sc.GetIdentifier())
			policy.SetServerConnectionClosed(sc.GetIdentifier(), sc.GetAddress(), CurrentTime())
			debugPeerf("server %s closed, retry in %dms",
				sc.GetIdentifier(), policy.MillisecondsUntilRetry(CurrentTime()))
		}
		if s.serverConnection == sc {
			s.serverConnection = nil
		}
	}
	s.promotePrimaryServer(nil)
	s.refreshPrimaryServerIdentity()
	s.mu.Unlock()
}

func (s *Session) SubmitDiskTask(task TransferCallable) {
	if task == nil {
		return
	}
	entry := &diskTask{callable: task, transfer: task.Transfer()}
	s.diskMu.Lock()
	s.diskTasks = append(s.diskTasks, entry)
	s.diskMu.Unlock()
}

func (s *Session) StartSearch(params SearchParams) (SearchHandle, error) {
	params = normalizeSearchParams(params)
	if params.Query == "" {
		return SearchHandle{}, NewError(IllegalArgument)
	}

	s.searchMu.Lock()
	if s.activeSearch != nil && s.activeSearch.state == SearchStateRunning {
		s.searchMu.Unlock()
		return SearchHandle{}, errors.New("search already in progress")
	}
	s.nextSearchID++
	searchID := s.nextSearchID
	task := newSearchTask(searchID, params, CurrentTime())
	s.activeSearch = task
	s.searchMu.Unlock()

	started := false
	if params.Scope&SearchScopeServer != 0 {
		if s.sendSearchRequest(task, params) {
			started = true
		}
	}
	if params.Scope&SearchScopeDHT != 0 {
		if s.startDHTSearch(task, params) {
			started = true
		}
	}
	if !started {
		task.fail(errors.New("no search backends available"))
		return SearchHandle{session: s, id: searchID}, nil
	}
	return SearchHandle{session: s, id: searchID}, nil
}

func (s *Session) SearchSnapshot() SearchSnapshot {
	s.searchMu.Lock()
	task := s.activeSearch
	s.searchMu.Unlock()
	if task == nil {
		return SearchSnapshot{}
	}
	return task.snapshot()
}

func (s *Session) StopSearch(id uint32) error {
	s.searchMu.Lock()
	task := s.activeSearch
	if task == nil || (id != 0 && task.id != id) {
		s.searchMu.Unlock()
		return nil
	}
	s.activeSearch = nil
	s.searchMu.Unlock()
	task.stop()
	return nil
}

func (s *Session) sendSearchRequest(task *searchTask, params SearchParams) bool {
	searchPacket := serverproto.SearchRequest{
		Query:              params.Query,
		MinSize:            params.MinSize,
		MaxSize:            params.MaxSize,
		MinSources:         params.MinSources,
		MinCompleteSources: params.MinCompleteSources,
		FileType:           params.FileType,
		Extension:          params.Extension,
	}
	sent := false
	for _, sc := range s.activeServerConnections() {
		if sc == nil || !sc.IsHandshakeCompleted() {
			continue
		}
		sc.SendSearchRequest(&searchPacket)
		sent = true
	}
	// #2: also broadcast the query over the server UDP socket. eMule uses
	// OP_GLOBSEARCHREQ3 to reach servers without keeping a TCP session per
	// search; results arrive via onUDPSearchResults and merge into this task.
	if s.SendUDPSearchRequest(&searchPacket) {
		sent = true
	}
	if sent {
		task.mu.Lock()
		task.serverBusy = true
		task.updatedAt = CurrentTime()
		task.deadlineAt = CurrentTime() + Seconds(int64(s.settings.ServerSearchTimeout))
		task.mu.Unlock()
	}
	return sent
}

func (s *Session) startDHTSearch(task *searchTask, params SearchParams) bool {
	if s.dhtTracker == nil {
		return false
	}
	keywords := parseSearchQuery(params.Query).Keywords(4)
	if len(keywords) == 0 {
		return false
	}
	task.beginDHT(keywords)
	started := false
	for _, keyword := range keywords {
		keywordHash, err := protocol.HashFromData([]byte(strings.ToLower(keyword)))
		if err != nil {
			task.finishDHTKeyword()
			continue
		}
		// A traversal listener can fire more than once (e.g. when done() is
		// reached from both a reply and a timeout). Count each keyword exactly
		// once so dhtBusy never clears while another lookup is still running.
		var finishOnce sync.Once
		finish := func() { finishOnce.Do(task.finishDHTKeyword) }
		// SEARCH_RES batches arrive while the traversal is still running. Merge
		// them immediately so the snapshot shows results progressively; only the
		// final listener marks this keyword finished.
		merge := func(entries []kadproto.SearchEntry) {
			s.searchMu.Lock()
			current := s.activeSearch
			s.searchMu.Unlock()
			if current == nil || current != task {
				return
			}
			for _, entry := range entries {
				result := makeSearchResultFromKAD(entry)
				if !matchesSearchFilters(result, params) {
					continue
				}
				if !s.applySearchRating(&result) {
					continue
				}
				task.mergeResult(result)
			}
		}
		ok := s.dhtTracker.SearchKeywordsWithProgress(keywordHash, func(entries []kadproto.SearchEntry) {
			merge(entries)
			finish()
		}, merge)
		if ok {
			started = true
		} else {
			finish()
		}
	}
	if !started {
		task.finishDHT()
	}
	return started
}

func (s *Session) OnServerSearchResult(sc *ServerConnection, result *serverproto.SearchResult) {
	if result == nil {
		return
	}
	s.searchMu.Lock()
	task := s.activeSearch
	s.searchMu.Unlock()
	if task == nil || task.state != SearchStateRunning {
		return
	}
	params := task.params
	for _, entry := range result.Results {
		searchResult := makeSearchResultFromServer(entry)
		if !matchesSearchFilters(searchResult, params) {
			continue
		}
		if !s.applySearchRating(&searchResult) {
			continue
		}
		task.mergeResult(searchResult)
	}
	if result.MoreResults && sc != nil {
		sc.SendSearchMore()
		task.setDeadline(CurrentTime() + Seconds(int64(s.settings.ServerSearchTimeout)))
		return
	}
	task.setDeadline(CurrentTime() + Seconds(2))
}

func (s *Session) tickSearches(now int64) {
	s.searchMu.Lock()
	task := s.activeSearch
	s.searchMu.Unlock()
	if task == nil {
		return
	}
	task.onTick(now)
}

func matchesSearchFilters(result SearchResult, params SearchParams) bool {
	if !parseSearchQuery(params.Query).Matches(result) {
		return false
	}
	if params.MinSize > 0 && result.FileSize > 0 && result.FileSize < params.MinSize {
		return false
	}
	if params.MaxSize > 0 && result.FileSize > params.MaxSize {
		return false
	}
	if params.MinSources > 0 && result.Sources < params.MinSources {
		return false
	}
	if params.MinCompleteSources > 0 && result.CompleteSources < params.MinCompleteSources {
		return false
	}
	if len(params.FileTypes) > 0 || len(params.Extensions) > 0 {
		typeHit := len(params.FileTypes) > 0 && containsFold(params.FileTypes, result.FileType)
		extensionHit := len(params.Extensions) > 0 &&
			containsFold(params.Extensions, strings.TrimPrefix(result.Extension, "."))
		if !typeHit && !extensionHit {
			return false
		}
	}
	if len(params.Sources) > 0 && !matchesSourceFilter(result.Source, params.Sources) {
		return false
	}
	return true
}

func containsFold(values []string, needle string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimPrefix(strings.TrimSpace(value), "."), strings.TrimSpace(needle)) {
			return true
		}
	}
	return false
}

func matchesSourceFilter(source SearchResultSource, filters []string) bool {
	for _, filter := range filters {
		switch strings.ToLower(strings.TrimSpace(filter)) {
		case "server", "all":
			if source&SearchResultServer != 0 {
				return true
			}
		case "kad":
			if source&SearchResultKAD != 0 {
				return true
			}
		case "both":
			if source == (SearchResultServer | SearchResultKAD) {
				return true
			}
		}
	}
	return false
}

func (s *Session) RemoveDiskTask(transfer *Transfer) {
	if transfer == nil {
		return
	}
	s.diskMu.Lock()
	defer s.diskMu.Unlock()
	dst := s.diskTasks[:0]
	for _, task := range s.diskTasks {
		if task.transfer == transfer {
			task.done = true
			continue
		}
		dst = append(dst, task)
	}
	s.diskTasks = dst
}

func (s *Session) processDiskTasks() {
	var nextTask *diskTask
	s.diskMu.Lock()
	for _, task := range s.diskTasks {
		if task == nil || task.done {
			continue
		}
		if !task.running {
			task.running = true
			nextTask = task
		}
		break
	}
	s.diskMu.Unlock()
	if nextTask != nil {
		go func(t *diskTask) {
			res := t.callable.Call()
			s.diskResults <- diskTaskResult{task: t, result: res}
		}(nextTask)
	}
	for {
		select {
		case result := <-s.diskResults:
			if result.task == nil || result.task.done {
				continue
			}
			s.diskMu.Lock()
			result.task.done = true
			dst := s.diskTasks[:0]
			for _, task := range s.diskTasks {
				if task != result.task {
					dst = append(dst, task)
				}
			}
			s.diskTasks = dst
			s.diskMu.Unlock()
			if result.result != nil {
				result.result.OnCompleted()
			}
		default:
			return
		}
	}
}

func (s *Session) diskTaskCount() int {
	s.diskMu.Lock()
	defer s.diskMu.Unlock()
	return len(s.diskTasks)
}

func (s *Session) SetDHTTracker(tracker *DHTTracker) {
	s.dhtTracker = tracker
	if tracker != nil {
		// #7: advertise our TCP port to buddies and relay buddy callbacks into
		// the same source-discovery path used by server callbacks.
		tracker.mu.Lock()
		tracker.buddyTCPPort = s.settings.ListenPort
		tracker.onCallback = func(peer protocol.Endpoint) {
			s.HandleCallbackRequest(peer)
		}
		tracker.onBuddyCallback = func(buddy *net.TCPAddr, check, file [16]byte, requester protocol.Endpoint) {
			s.ForwardKadCallback(buddy, check, file, requester)
		}
		tracker.mu.Unlock()
	}
}

func (s *Session) GetDHTTracker() *DHTTracker {
	return s.dhtTracker
}

// ForwardKadCallback relays a KAD callback request to the peer we act as buddy
// for. eMule forwards <check><file><ip><port> with OP_CALLBACK over the already
// established TCP connection instead of dialing the requester itself.
func (s *Session) ForwardKadCallback(buddy *net.TCPAddr, check, file [16]byte, requester protocol.Endpoint) bool {
	if s == nil || buddy == nil || !requester.Defined() {
		return false
	}
	pc := s.connectionByTCP(buddy)
	if pc == nil {
		debugPeerf("kad callback: no tcp connection for buddy %s", buddy.String())
		return false
	}
	pc.SendCallback(check, file, requester)
	debugPeerf("kad callback: forwarded %s to buddy %s", requester.String(), buddy.String())
	return true
}

// HandleFileCallback consumes an OP_CALLBACK forwarded by a buddy: the source
// hash identifies the transfer, so only that transfer gets the endpoint.
func (s *Session) HandleFileCallback(check, file [16]byte, requester protocol.Endpoint) int {
	if s == nil || !requester.Defined() || IsLowID(requester.IP()) {
		return 0
	}
	hash, err := protocol.HashFromBytes(file[:])
	if err != nil {
		return 0
	}
	transfer := s.LookupTransfer(hash)
	if transfer == nil {
		debugPeerf("kad callback: no transfer for %s", hash.String())
		return 0
	}
	if err := transfer.AddPeer(requester, int(PeerDHT)); err != nil {
		return 0
	}
	debugPeerf("kad callback: %s added to %s (check=%x)", requester.String(), hash.String(), check[:4])
	return 1
}

// connectionByTCP finds the peer connection whose endpoint matches addr.
func (s *Session) connectionByTCP(addr *net.TCPAddr) *PeerConnection {
	want := protocol.EndpointFromInet(addr)
	for _, c := range s.snapshotConnections() {
		if c == nil {
			continue
		}
		if c.endpoint.IP() == want.IP() && c.endpoint.Port() == want.Port() {
			return c
		}
	}
	return nil
}

func (s *Session) UploadQueue() *UploadQueue {
	s.initUploadQueue()
	return s.uploadQueue
}

func (s *Session) Credits() *PeerCreditManager {
	if s == nil {
		return nil
	}
	if s.credits == nil {
		s.credits = NewPeerCreditManager()
	}
	return s.credits
}

func (s *Session) SetFriendSlot(hash protocol.Hash, enabled bool) {
	if s == nil || hash.Equal(protocol.Invalid) {
		return
	}
	key := hash.String()
	if enabled {
		s.friendSlots[key] = true
		return
	}
	delete(s.friendSlots, key)
}

func (s *Session) IsFriendSlot(hash protocol.Hash) bool {
	if s == nil || hash.Equal(protocol.Invalid) {
		return false
	}
	return s.friendSlots[hash.String()]
}

func (s *Session) friendSlotSnapshot() []protocol.Hash {
	if s == nil {
		return nil
	}
	out := make([]protocol.Hash, 0, len(s.friendSlots))
	for key, enabled := range s.friendSlots {
		if !enabled {
			continue
		}
		hash, err := protocol.HashFromString(key)
		if err != nil {
			continue
		}
		out = append(out, hash)
	}
	return out
}

func (s *Session) applyFriendSlotSnapshot(values []protocol.Hash) {
	if s == nil {
		return
	}
	s.friendSlots = make(map[string]bool, len(values))
	for _, hash := range values {
		if hash.Equal(protocol.Invalid) {
			continue
		}
		s.friendSlots[hash.String()] = true
	}
}

func (s *Session) OnServerIDChange(sc *ServerConnection, clientID, tcpFlags, auxPort int32) {
	if sc == nil {
		return
	}
	var kick []*Transfer
	var offerFiles []serverproto.OfferFile
	var shareable []*Transfer
	s.mu.Lock()
	if policy := s.serverConnectionPolicy[sc.GetIdentifier()]; policy != nil {
		// A completed login clears the backoff ramp, so a server that dropped us
		// repeatedly over a long session still retries fast next time.
		policy.MarkLoginSucceeded(sc.GetIdentifier(), sc.GetAddress())
	}
	if s.serverConnection == nil || !s.serverConnection.IsHandshakeCompleted() {
		s.serverConnection = sc
	}
	if s.serverConnection == sc || s.clientID == 0 {
		s.clientID = clientID
		s.tcpFlags = tcpFlags
		s.auxPort = auxPort
	}
	for _, transfer := range s.transfers {
		if transfer == nil || transfer.IsPaused() || transfer.IsAborted() {
			continue
		}
		if transfer.CanUpload() {
			// Offer part files too, not just finished ones. eMule shares what it
			// has while it downloads ("共享未完成文件"), and that is the only way a
			// new client ever uploads anything: without an offer no peer learns we
			// are a source, we never serve a byte, and every queue on the network
			// scores us as a pure leech.
			offerFiles = append(offerFiles, serverproto.OfferFile{
				Hash: transfer.GetHash(),
				Name: transfer.FileName(),
				Size: transfer.Size(),
			})
			shareable = append(shareable, transfer)
			continue
		}
		kick = append(kick, transfer)
	}
	s.mu.Unlock()
	if len(offerFiles) > 0 {
		packet := serverproto.NewOfferFiles(clientID, s.GetListenPort(), offerFiles)
		sc.SendOfferFiles(&packet)
	}
	// #14: re-publish to KAD on every ID change. A login that first arrives as
	// a low ID cannot publish (its "IP" is a server token); once a high ID is
	// assigned, these sources become routable and must be announced.
	for _, transfer := range shareable {
		s.PublishTransferToDHT(transfer)
	}
	for _, transfer := range kick {
		s.RequestSourcesNow(transfer)
	}
}

// PublishTransferToDHT announces our own TCP endpoint as a KAD source for the
// transfer. eMule publishes to both the server and KAD whenever a file becomes
// shareable; doing only the server leaves KAD-only peers unable to find us.
// Publishing needs a routable (high-ID) address: a low ID is a server-assigned
// number, not an IP, and publishing it would poison the network with a bogus
// source. When no DHT tracker is configured this is a silent no-op.
func (s *Session) PublishTransferToDHT(t *Transfer) bool {
	if s == nil || t == nil || s.dhtTracker == nil || t.IsPaused() || t.IsAborted() || !t.CanUpload() {
		return false
	}
	endpoint, ok := publishableSourceEndpoint(s.GetClientID(), s.GetListenPort())
	if !ok {
		return false
	}
	ok = s.dhtTracker.PublishSource(t.GetHash(), endpoint, t.Size())
	debugPeerf("dht publish file=%s endpoint=%s ok=%t", t.GetHash().String(), endpoint.String(), ok)
	return ok
}

// publishableSourceEndpoint converts a server ID into the TCP endpoint we may
// advertise to KAD. Only a high ID is a real IP; low IDs and zero are server
// tokens and must never be published as source addresses.
func publishableSourceEndpoint(clientID int32, port int) (protocol.Endpoint, bool) {
	if clientID == 0 || IsLowID(clientID) || port <= 0 {
		return protocol.Endpoint{}, false
	}
	endpoint := protocol.NewEndpoint(clientID, port)
	return endpoint, endpoint.Defined()
}

// PublishTransferToServer announces one transfer as a source. eMule re-offers
// a file whenever it becomes shareable, so peers can start pulling the parts we
// already hold while the download is still running.
func (s *Session) PublishTransferToServer(t *Transfer) {
	if s == nil || t == nil {
		return
	}
	s.mu.Lock()
	sc := s.serverConnection
	clientID := s.clientID
	s.mu.Unlock()
	if sc == nil || !sc.IsHandshakeCompleted() || clientID == 0 || t.IsPaused() || t.IsAborted() {
		return
	}
	if !t.CanUpload() {
		// Nothing to serve yet; offering an empty part file only makes seekers
		// connect and get OutOfParts.
		return
	}
	packet := serverproto.NewOfferFiles(clientID, s.GetListenPort(), []serverproto.OfferFile{{
		Hash: t.GetHash(),
		Name: t.FileName(),
		Size: t.Size(),
	}})
	sc.SendOfferFiles(&packet)
}

func (s *Session) ensureServerConnectionPolicy(identifier string) *ServerConnectionPolicy {
	if identifier == "" {
		return nil
	}
	if policy := s.serverConnectionPolicy[identifier]; policy != nil {
		return policy
	}
	policy := NewServerConnectionPolicy(5, 5)
	s.serverConnectionPolicy[identifier] = &policy
	return &policy
}

func (s *Session) connectConfiguredServers(currentSessionTime int64) {
	s.mu.Lock()
	configured := make(map[string]*net.TCPAddr, len(s.configuredServers))
	for identifier, address := range s.configuredServers {
		configured[identifier] = address
	}
	s.mu.Unlock()
	for identifier, address := range configured {
		if identifier == "" || address == nil {
			continue
		}
		s.mu.Lock()
		existing := s.serverConnections[identifier]
		s.mu.Unlock()
		if existing != nil && !existing.IsDisconnecting() {
			continue
		}
		s.mu.Lock()
		policy := s.ensureServerConnectionPolicy(identifier)
		candidate := policy.GetConnectCandidate(currentSessionTime)
		s.mu.Unlock()
		if candidate == nil || candidate.Identifier == "" || candidate.Address == nil {
			continue
		}
		sc := NewServerConnection(candidate.Identifier, candidate.Address, s)
		s.mu.Lock()
		s.serverConnections[identifier] = sc
		s.promotePrimaryServer(sc)
		s.mu.Unlock()
		// Async on purpose: this runs inside SecondTick, and a blocking dial to a
		// dead server would stall every transfer for the connect timeout.
		if err := sc.ConnectAsync(); err != nil {
			sc.Close(IOException)
		}
	}
}

func (s *Session) activeServerConnections() []*ServerConnection {
	s.mu.Lock()
	defer s.mu.Unlock()
	res := make([]*ServerConnection, 0, len(s.serverConnections)+1)
	seen := make(map[*ServerConnection]struct{}, len(s.serverConnections)+1)
	if s.serverConnection != nil {
		res = append(res, s.serverConnection)
		seen[s.serverConnection] = struct{}{}
	}
	for _, sc := range s.serverConnections {
		if sc == nil {
			continue
		}
		if _, ok := seen[sc]; ok {
			continue
		}
		res = append(res, sc)
		seen[sc] = struct{}{}
	}
	return res
}

func (s *Session) snapshotTransfers() []*Transfer {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*Transfer, 0, len(s.transfers))
	for _, t := range s.transfers {
		out = append(out, t)
	}
	return out
}

func (s *Session) snapshotConnections() []*PeerConnection {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*PeerConnection, len(s.connections))
	copy(out, s.connections)
	return out
}

func (s *Session) promotePrimaryServer(sc *ServerConnection) {
	if sc != nil {
		if s.serverConnection == nil || s.serverConnection.IsDisconnecting() || !s.serverConnection.IsHandshakeCompleted() {
			s.serverConnection = sc
		}
		return
	}
	if s.serverConnection != nil && !s.serverConnection.IsDisconnecting() {
		return
	}
	for _, candidate := range s.serverConnections {
		if candidate != nil && !candidate.IsDisconnecting() {
			s.serverConnection = candidate
			return
		}
	}
	s.serverConnection = nil
}

func (s *Session) refreshPrimaryServerIdentity() {
	if s.serverConnection != nil && s.serverConnection.IsHandshakeCompleted() {
		s.clientID = s.serverConnection.ClientID()
		s.tcpFlags = s.serverConnection.TCPFlags()
		s.auxPort = s.serverConnection.AuxPort()
		return
	}
	for _, sc := range s.serverConnections {
		if sc != nil && sc.IsHandshakeCompleted() {
			s.serverConnection = sc
			s.clientID = sc.ClientID()
			s.tcpFlags = sc.TCPFlags()
			s.auxPort = sc.AuxPort()
			return
		}
	}
	s.clientID = 0
	s.tcpFlags = 0
	s.auxPort = 0
}
