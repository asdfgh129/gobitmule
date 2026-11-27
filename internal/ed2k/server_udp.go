package goed2k

// #2: eD2k server UDP transport. eMule binds the server UDP socket at TCP
// port + 4 and uses it for global searches and glob source requests, with an
// optional RC4 envelope keyed by the server's UDP key. This file owns that
// socket and feeds decoded results back into the session.

import (
	"net"
	"sync"

	"github.com/asdfgh129/gobitmule/internal/ed2k/protocol"
	serverproto "github.com/asdfgh129/gobitmule/internal/ed2k/protocol/server"
)

// ServerUDP holds the server-facing UDP socket. Send uses the connected
// server's UDP endpoint; baseKey==0 means plaintext (server did not supply a
// UDP key), matching eMule's IsServerCryptLayerUDPEnabled()==false path.
type ServerUDP struct {
	mu        sync.Mutex
	conn      *net.UDPConn
	baseKey   uint32
	stopCh    chan struct{}
	once      sync.Once
	onResults func(protocol.Hash, []protocol.Endpoint)
	onSearch  func([]serverproto.SharedFileEntry)
}

func NewServerUDP() *ServerUDP {
	return &ServerUDP{stopCh: make(chan struct{})}
}

// SetUDPKey installs the server's UDP key (0 disables encryption).
func (u *ServerUDP) SetUDPKey(key uint32) {
	u.mu.Lock()
	u.baseKey = key
	u.mu.Unlock()
}

// UDPPort returns the bound port or 0 when not started.
func (u *ServerUDP) UDPPort() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.conn == nil {
		return 0
	}
	if addr, ok := u.conn.LocalAddr().(*net.UDPAddr); ok {
		return addr.Port
	}
	return 0
}

// Start binds the UDP socket. port<0 disables it (offline tests).
func (u *ServerUDP) Start(port int) error {
	if port < 0 {
		return nil
	}
	u.mu.Lock()
	if u.conn != nil {
		u.mu.Unlock()
		return nil
	}
	conn, err := net.ListenUDP("udp", &net.UDPAddr{Port: port})
	if err != nil && port > 0 {
		conn, err = net.ListenUDP("udp", &net.UDPAddr{Port: 0})
	}
	if err != nil {
		u.mu.Unlock()
		return err
	}
	u.conn = conn
	onResults, onSearch := u.onResults, u.onSearch
	u.mu.Unlock()
	go u.readLoop(conn, onResults, onSearch)
	return nil
}

func (u *ServerUDP) readLoop(conn *net.UDPConn,
	onResults func(protocol.Hash, []protocol.Endpoint),
	onSearch func([]serverproto.SharedFileEntry)) {
	buf := make([]byte, 8192)
	for {
		select {
		case <-u.stopCh:
			return
		default:
		}
		n, _, err := conn.ReadFromUDP(buf)
		if err != nil {
			select {
			case <-u.stopCh:
				return
			default:
				continue
			}
		}
		datagram := append([]byte(nil), buf[:n]...)
		// Read the key per datagram: OP_SERVERIDENT installs it after the
		// socket is already running, so a startup snapshot would leave every
		// encrypted answer undecryptable for the whole session.
		u.mu.Lock()
		baseKey := u.baseKey
		u.mu.Unlock()
		plain, ok := serverproto.DecryptServerDatagram(datagram, baseKey)
		if !ok {
			// Plaintext when no key, encrypted when we could decrypt.
			plain = datagram
		}
		if onResults != nil {
			for hash, endpoints := range serverproto.ParseUDPFoundSources(plain) {
				onResults(hash, endpoints)
			}
		}
		if onSearch != nil {
			if entries, err := serverproto.ParseUDPSearchResults(plain); err == nil && len(entries) > 0 {
				onSearch(entries)
			}
		}
	}
}

// SendTo sends a plaintext datagram (optionally RC4-wrapped) to addr.
func (u *ServerUDP) SendTo(addr *net.UDPAddr, datagram []byte) bool {
	u.mu.Lock()
	conn := u.conn
	baseKey := u.baseKey
	u.mu.Unlock()
	if conn == nil || addr == nil {
		return false
	}
	wire := datagram
	if baseKey != 0 {
		encrypted, err := serverproto.EncryptServerDatagram(datagram, baseKey)
		if err != nil {
			return false
		}
		wire = encrypted
	}
	_, err := conn.WriteToUDP(wire, addr)
	return err == nil
}

func (u *ServerUDP) Close() {
	u.once.Do(func() { close(u.stopCh) })
	u.mu.Lock()
	if u.conn != nil {
		_ = u.conn.Close()
		u.conn = nil
	}
	u.mu.Unlock()
}
