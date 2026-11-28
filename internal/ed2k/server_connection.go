package goed2k

import (
	"net"
	"sync"

	"github.com/asdfgh129/gobitmule/internal/ed2k/protocol"
	serverproto "github.com/asdfgh129/gobitmule/internal/ed2k/protocol/server"
)

type ServerConnection struct {
	Connection
	lastPingTime       int64
	handshakeCompleted bool
	identifier         string
	address            *net.TCPAddr
	clientID           int32
	tcpFlags           int32
	auxPort            int32
	combiner           protocol.PacketCombiner

	// #16: OP_SERVERSTATUS and OP_SERVERMESSAGE used to be dropped after a
	// debug log, so the console could not display the server's user/file counts
	// or its welcome/warning banner. The session pump writes these; the REST
	// snapshot goroutine reads them, hence the mutex.
	serverInfoMu sync.Mutex
	// serverListRequested guards the one-shot OP_GETSERVERLIST that makes the
	// server answer with OP_SERVERIDENT.
	serverListRequested bool
	usersCount          int32
	filesCount          int32
	lastMessage         string
	// OP_SERVERIDENT carries the UDP capability flags and the obfuscation key
	// used by server datagrams; eMule keeps them on the CServer object.
	udpFlags     uint32
	udpKey       uint32
	udpKeyIP     uint32
	serverName   string
	// reportedIP is the public IP OP_IDCHANGE told us we have (offset 12).
	// CServer::GetServerKeyUDP() binds the UDP obfuscation key to this value,
	// not to the (possibly LOW_ID) clientID.
	reportedIP uint32
	// hasServerIdent is set once OP_SERVERIDENT has been parsed on this
	// connection. A connected server that advertises no UDP capability at all
	// (udpFlags == 0) is indistinguishable from "not answered yet" by the flags
	// alone, and the two cases must be told apart: only the latter may fall
	// back to the capability bits cached in server.met.
	hasServerIdent bool

	// Asynchronous dial state. connectConfiguredServers runs inside SecondTick,
	// so a blocking dial to a dead server froze every transfer for the whole
	// connect timeout (15s) on each retry.
	dialDone   chan struct{}
	dialSocket net.Conn
	dialErr    error
}

func NewServerConnection(identifier string, address *net.TCPAddr, session *Session) *ServerConnection {
	return &ServerConnection{
		Connection:   NewConnection(session),
		lastPingTime: CurrentTime(),
		identifier:   identifier,
		address:      address,
		combiner:     serverproto.NewPacketCombiner(),
	}
}

func (s *ServerConnection) Connect() error {
	if s.address == nil {
		return NewError(InternalError)
	}
	if err := s.Connection.Connect(s.address); err != nil {
		return err
	}
	s.SendLoginRequest()
	return nil
}

// ConnectAsync dials the server on a background goroutine. The session loop
// publishes the socket on a later pump tick and sends the login request then,
// so an unreachable server never blocks the whole engine.
func (s *ServerConnection) ConnectAsync() error {
	if s.address == nil {
		return NewError(InternalError)
	}
	done := make(chan struct{})
	s.dialDone = done
	addr := s.address
	go func() {
		defer close(done)
		socket, _, err := s.openSocket(addr)
		s.dialSocket = socket
		s.dialErr = err
	}()
	return nil
}

// Dialing reports whether an asynchronous dial is still in flight.
func (s *ServerConnection) Dialing() bool { return s.dialDone != nil }

// pumpDial publishes a finished asynchronous dial and sends the login request.
// It returns true while the dial is still running.
func (s *ServerConnection) pumpDial() bool {
	if s.dialDone == nil {
		return false
	}
	select {
	case <-s.dialDone:
	default:
		return true
	}
	s.dialDone = nil
	if s.dialErr != nil {
		debugPeerf("server %s dial failed: %v", s.identifier, s.dialErr)
		s.Close(IOException)
		return false
	}
	if s.IsDisconnecting() || s.dialSocket == nil {
		if s.dialSocket != nil {
			_ = s.dialSocket.Close()
		}
		return false
	}
	s.adoptSocket(s.dialSocket, false)
	s.dialSocket = nil
	debugPeerf("server %s connected, sending login", s.identifier)
	s.SendLoginRequest()
	return false
}

func (s *ServerConnection) OnServerIDChange(clientID, tcpFlags, auxPort int32, reportedIP uint32) {
	debugPeerf("server %s <- IdChange clientID=%d reportedIP=%d", s.identifier, clientID, reportedIP)
	s.clientID = clientID
	s.tcpFlags = tcpFlags
	s.auxPort = auxPort
	s.reportedIP = reportedIP
	s.handshakeCompleted = true
	s.session.OnServerIDChange(s, clientID, tcpFlags, auxPort, reportedIP)
}

func (s *ServerConnection) OnDisconnect(ec BaseErrorCode) {
	debugPeerf("server %s disconnect code=%d", s.identifier, ec.Code())
	s.handshakeCompleted = false
	s.session.OnServerConnectionClosed(s, ec)
}

func (s *ServerConnection) SecondTick(currentSessionTime int64) {
	s.Connection.SecondTick(currentSessionTime)
	if s.session.settings.ServerPingTimeout > 0 {
		currentTime := CurrentTime()
		if s.MillisecondsSinceLastReceive() > s.session.settings.ServerPingTimeout*1500 {
			s.Close(ConnectionTimeout)
		} else if currentTime-s.lastPingTime > s.session.settings.ServerPingTimeout*1000 {
			s.lastPingTime = currentTime
			s.SendGetList()
		}
	}
}

func (s *ServerConnection) Endpoint() protocol.Endpoint {
	return protocol.Endpoint{}
}

func (s *ServerConnection) SendFileSourcesRequest(hash protocol.Hash, size int64) {
	debugPeerf("server %s -> GetFileSources %s size=%d", s.identifier, hash.String(), size)
	packet := serverproto.GetFileSources{
		Hash:    hash,
		LowPart: int32(LowPart(size)),
		HiPart:  int32(HiPart(size)),
	}
	if raw, err := s.combiner.Pack("server.GetFileSources", &packet); err == nil {
		s.QueuePacket(raw)
	}
}

func (s *ServerConnection) SendSearchRequest(packet *serverproto.SearchRequest) {
	if packet == nil {
		return
	}
	debugPeerf("server %s -> SearchRequest query=%q", s.identifier, packet.Query)
	if raw, err := s.combiner.Pack("server.SearchRequest", packet); err == nil {
		s.QueuePacket(raw)
	}
}

func (s *ServerConnection) SendSearchMore() {
	debugPeerf("server %s -> SearchMore", s.identifier)
	packet := serverproto.SearchMore{}
	if raw, err := s.combiner.Pack("server.SearchMore", &packet); err == nil {
		s.QueuePacket(raw)
	}
}

// SendGetServerList asks the server for its server.met plus OP_SERVERIDENT.
// The list itself is ignored (WithoutDataPacket); the ident carries the UDP
// capability flags and obfuscation key.
func (s *ServerConnection) SendGetServerList() {
	packet := serverproto.GetList{}
	raw, err := s.combiner.Pack("server.GetList", &packet)
	if err != nil {
		debugPeerf("server %s -> GetServerList pack failed: %v", s.identifier, err)
		return
	}
	debugPeerf("server %s -> GetServerList (asks for OP_SERVERIDENT)", s.identifier)
	s.QueuePacket(raw)
}

func (s *ServerConnection) SendLoginRequest() {
	packet := serverproto.NewLoginRequest(s.session.GetUserAgent(), s.session.GetListenPort(), s.session.GetUDPPort(), s.session.GetClientName())
	raw, err := s.combiner.Pack("server.LoginRequest", &packet)
	if err != nil {
		debugPeerf("server %s -> LoginRequest pack failed: %v", s.identifier, err)
		return
	}
	// Log the wire bytes: servers reject a malformed login by closing the socket
	// without an answer, and "disconnect code=10" says nothing about why.
	debugPeerf("server %s -> LoginRequest bytes=%d hex=%s name=%q port=%d",
		s.identifier, len(raw), hexPrefix(raw, 64), s.session.GetClientName(), s.session.GetListenPort())
	s.QueuePacket(raw)
}

func (s *ServerConnection) SendCallbackRequest(clientID int32) {
	packet := serverproto.CallbackRequest{ClientID: clientID}
	if raw, err := s.combiner.Pack("server.CallbackRequest", &packet); err == nil {
		s.QueuePacket(raw)
	}
}

func (s *ServerConnection) SendGetList() {
	packet := serverproto.GetList{}
	if raw, err := s.combiner.Pack("server.GetList", &packet); err == nil {
		s.QueuePacket(raw)
	}
}

func (s *ServerConnection) SendOfferFiles(packet *serverproto.OfferFiles) {
	if packet == nil || len(packet.Entries) == 0 {
		return
	}
	debugPeerf("server %s -> OfferFiles count=%d", s.identifier, len(packet.Entries))
	if raw, err := s.combiner.Pack("server.OfferFiles", packet); err == nil {
		s.QueuePacket(raw)
	}
}

func (s *ServerConnection) ProcessIncoming() error {
	_, packets, err := s.DecodeFrames(&s.combiner)
	if err != nil {
		return err
	}
	for _, packet := range packets {
		switch value := packet.(type) {
		case *serverproto.IdChange:
			s.OnServerIDChange(value.ClientID, value.TCPFlags, value.AuxPort, value.ReportedIP)
			// eMule only receives OP_SERVERIDENT (and with it ST_UDPFLAGS /
			// ST_UDPKEY / ST_UDPKEYIP) after it asks for the server list:
			// "OP_SERVERIDENT - this is sent by the server only if we send a
			// OP_GETSERVERLIST" (ServerSocket.cpp). Without this request server
			// UDP can never learn the key and stays dead against servers that
			// require it, so send it once per connection.
			if value.ClientID != 0 && !s.serverListRequested {
				s.serverListRequested = true
				s.SendGetServerList()
			}
		case *serverproto.FoundFileSources:
			debugPeerf("server %s <- FoundFileSources hash=%s count=%d", s.identifier, value.Hash.String(), len(value.Sources))
			if transfer := s.session.LookupTransfer(value.Hash); transfer != nil {
				for _, ep := range value.Sources {
					if IsLowID(ep.IP()) {
						// #1: a low-ID source is unreachable directly; ask the
						// server to relay a callback instead of dropping it.
						s.session.RequestCallbackForLowID(ep.IP(), value.Hash)
						continue
					}
					_ = transfer.AddPeer(ep, int(PeerServer))
				}
			}
		case *serverproto.CallbackRequestIncoming:
			// #1: server relayed a low-ID peer's real endpoint; offer it to the
			// active transfers instead of only logging it.
			s.session.HandleCallbackRequest(value.Point)
		case *serverproto.CallbackRequestFailed:
			// The peer did not answer the callback; nothing else to do. Its
			// pending entry is bounded and the next source poll can retry.
			debugPeerf("server %s <- CallbackRequestFailed", s.identifier)
		case *serverproto.ServerIdent:
			s.serverInfoMu.Lock()
			s.udpFlags = value.UDPFlags
			s.udpKey = value.UDPKey
			s.udpKeyIP = value.UDPKeyIP
			s.serverName = value.Name
			s.hasServerIdent = true
			s.serverInfoMu.Unlock()
			// Feed the capability bits back into the session cache so a later
			// search can pick the right datagram opcode without waiting for yet
			// another handshake (mirrors eMule writing ST_UDPFLAGS to disk).
			s.session.RememberServerCapabilities(s.identifier, value.UDPFlags)
			debugPeerf("server %s <- ServerIdent name=%q version=%q udpFlags=0x%08x udpKey=%t",
				s.identifier, value.Name, value.Version, value.UDPFlags, value.UDPKey != 0)
			// eMule enables the server UDP envelope only when the server offers
			// obfuscation and bound the key to our public IP (CServer::GetServerKeyUDP).
			if value.UDPKey != 0 && value.SupportsUDPObfuscation() {
				s.session.SetServerUDPKey(value.UDPKey, value.UDPKeyIP)
			}
		case *serverproto.Status:
			s.serverInfoMu.Lock()
			s.usersCount = value.UsersCount
			s.filesCount = value.FilesCount
			s.serverInfoMu.Unlock()
			debugPeerf("server %s <- Status users=%d files=%d", s.identifier, value.UsersCount, value.FilesCount)
		case *serverproto.Message:
			message := value.AsString()
			s.serverInfoMu.Lock()
			s.lastMessage = message
			s.serverInfoMu.Unlock()
			debugPeerf("server %s <- Message %q", s.identifier, message)
		case *serverproto.SearchResult:
			debugPeerf("server %s <- SearchResult count=%d more=%t", s.identifier, len(value.Results), value.MoreResults)
			s.session.OnServerSearchResult(s, value)
		}
	}
	return nil
}

func (s *ServerConnection) GetIdentifier() string {
	return s.identifier
}

func (s *ServerConnection) GetAddress() *net.TCPAddr {
	return s.address
}

func (s *ServerConnection) IsHandshakeCompleted() bool {
	return s.handshakeCompleted
}

func (s *ServerConnection) ClientID() int32 {
	return s.clientID
}

func (s *ServerConnection) TCPFlags() int32 {
	return s.tcpFlags
}

func (s *ServerConnection) AuxPort() int32 {
	return s.auxPort
}

// UDPFlags reports the SRV_UDPFLG_* capability bits from OP_SERVERIDENT.
func (s *ServerConnection) UDPFlags() uint32 {
	s.serverInfoMu.Lock()
	defer s.serverInfoMu.Unlock()
	return s.udpFlags
}

// HasServerIdent reports whether OP_SERVERIDENT has been parsed on this
// connection. Until then a zero UDPFlags value says nothing about the server.
func (s *ServerConnection) HasServerIdent() bool {
	s.serverInfoMu.Lock()
	defer s.serverInfoMu.Unlock()
	return s.hasServerIdent
}

// ServerName reports the name advertised by OP_SERVERIDENT, if any.
func (s *ServerConnection) ServerName() string {
	s.serverInfoMu.Lock()
	defer s.serverInfoMu.Unlock()
	return s.serverName
}

// ServerInfo returns the latest OP_SERVERSTATUS counts and OP_SERVERMESSAGE
// banner. #16: these used to be logged and discarded.
func (s *ServerConnection) ServerInfo() (users, files int32, message string) {
	s.serverInfoMu.Lock()
	defer s.serverInfoMu.Unlock()
	return s.usersCount, s.filesCount, s.lastMessage
}
