package goed2k

import (
	"crypto/md5"
	"crypto/rc4"
	"encoding/binary"
	"encoding/hex"
	"net"
	"testing"
	"time"

	"github.com/asdfgh129/gobitmule/internal/ed2k/protocol"
	serverproto "github.com/asdfgh129/gobitmule/internal/ed2k/protocol/server"
)

// Independent eMule-envelope helpers, written from EncryptedDatagramSocket.cpp.
// The protocol/server package has its own copies for the vector tests; keeping
// this one here lets the test drive the real UDP socket without exporting any
// production helper.
func emuleServerWrap(t *testing.T, plain []byte, baseKey uint32, serverToClient bool) []byte {
	t.Helper()
	direction := byte(0x6B) // MAGICVALUE_UDP_CLIENTSERVER
	if serverToClient {
		direction = 0xA5 // MAGICVALUE_UDP_SERVERCLIENT
	}
	randomPart := []byte{0x5C, 0xA7}
	keyData := make([]byte, 7)
	binary.LittleEndian.PutUint32(keyData[:4], baseKey)
	keyData[4] = direction
	copy(keyData[5:], randomPart)
	sum := md5.Sum(keyData)
	cipher, err := rc4.NewCipher(sum[:])
	if err != nil {
		t.Fatalf("rc4: %v", err)
	}
	scratch := make([]byte, 5+len(plain))
	binary.LittleEndian.PutUint32(scratch[:4], 0x13EF24D5)
	scratch[4] = 0 // padLen, disabled for UDP in eMule
	copy(scratch[5:], plain)
	out := make([]byte, 3+len(scratch))
	out[0] = 0x3C // non-protocol marker
	copy(out[1:3], randomPart)
	cipher.XORKeyStream(out[3:], scratch)
	return out
}

func emuleClientUnwrap(t *testing.T, wire []byte, baseKey uint32) []byte {
	t.Helper()
	keyData := make([]byte, 7)
	binary.LittleEndian.PutUint32(keyData[:4], baseKey)
	keyData[4] = 0x6B
	copy(keyData[5:], wire[1:3])
	sum := md5.Sum(keyData)
	cipher, err := rc4.NewCipher(sum[:])
	if err != nil {
		t.Fatalf("rc4: %v", err)
	}
	dec := make([]byte, len(wire)-3)
	cipher.XORKeyStream(dec, wire[3:])
	if len(dec) < 5 || binary.LittleEndian.Uint32(dec[:4]) != 0x13EF24D5 {
		t.Fatalf("eMule server could not recognize our envelope: % X", dec[:4])
	}
	pad := int(dec[4] & 0x0f)
	if len(dec) < 5+pad {
		t.Fatalf("bad pad length %d", pad)
	}
	return dec[5+pad:]
}

// The 0x92 branch must emit a bare search tree: no tag count, no tag set.
// A server that advertises EXT_GETFILES but not LARGEFILES reads this form.
func TestUDPSearchReqV2WireFormat(t *testing.T) {
	req := &serverproto.SearchRequest{Query: "debian"}
	wire, err := serverproto.UDPGlobSearchDatagram(req, serverproto.UDPFlagExtGetFiles)
	if err != nil {
		t.Fatalf("pack: %v", err)
	}
	if len(wire) < 3 || wire[0] != protocol.EdonkeyHeader || wire[1] != serverproto.OpGlobSearchReqV2 {
		t.Fatalf("header = % X, want E3 92", wire[:2])
	}
	// 0x90 starts with uint32 tag count 01 00 00 00; 0x92 starts with the
	// search tree's own <type=1><stringLen=6><"debian">. The leading 0x01 is
	// the search term type, not a tag count — the following bytes tell them
	// apart.
	if len(wire) < 8 {
		t.Fatalf("0x92 datagram too short: % X", wire)
	}
	if binary.LittleEndian.Uint32(wire[2:6]) == 1 {
		t.Fatalf("0x92 datagram unexpectedly carries a tag count: % X", wire)
	}
	if wire[2] != 0x01 || wire[3] != 0x06 || wire[4] != 0x00 || string(wire[5:11]) != "debian" {
		t.Fatalf("0x92 search body is not a bare search tree: % X", wire)
	}
	// With LARGEFILES added, eMule switches to the tagged 0x90 form.
	v3, err := serverproto.UDPGlobSearchDatagram(req, serverproto.UDPFlagExtGetFiles|serverproto.UDPFlagLargeFiles)
	if err != nil {
		t.Fatalf("pack v3: %v", err)
	}
	if v3[1] != serverproto.OpGlobSearchReq {
		t.Fatalf("v3 opcode = %#x, want 0x90", v3[1])
	}
	if binary.LittleEndian.Uint32(v3[2:6]) != 1 {
		t.Fatalf("0x90 tag count = %d, want 1", binary.LittleEndian.Uint32(v3[2:6]))
	}
}

// End-to-end through the real UDP socket: our 0x92 request is encrypted in a
// form an unmodified eMule server can decode, and its encrypted 0x99/0x9B
// answers are decrypted, parsed and delivered to the session callbacks.
func TestServerUDPEncryptedRoundTripAndEMuleAnswers(t *testing.T) {
	const baseKey = uint32(0x13572468)
	fakeServer, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("fake server: %v", err)
	}
	defer fakeServer.Close()

	u := NewServerUDP()
	u.SetUDPKey(baseKey)
	type result struct {
		hash      protocol.Hash
		endpoints []protocol.Endpoint
	}
	type search struct{ entries []serverproto.SharedFileEntry }
	results := make(chan result, 2)
	searches := make(chan search, 2)
	u.onResults = func(hash protocol.Hash, endpoints []protocol.Endpoint) {
		results <- result{hash: hash, endpoints: endpoints}
	}
	u.onSearch = func(entries []serverproto.SharedFileEntry) {
		searches <- search{entries: entries}
	}
	if err := u.Start(0); err != nil {
		t.Fatalf("ServerUDP.Start: %v", err)
	}
	defer u.Close()
	clientAddr := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: u.UDPPort()}
	if clientAddr.Port == 0 {
		t.Fatal("ServerUDP did not bind a port")
	}
	serverAddr := fakeServer.LocalAddr().(*net.UDPAddr)

	// 1) 0x92 search request, encrypted on the wire, decoded with eMule's key.
	req := &serverproto.SearchRequest{Query: "debian"}
	request, err := serverproto.UDPGlobSearchDatagram(req, serverproto.UDPFlagExtGetFiles)
	if err != nil {
		t.Fatalf("pack request: %v", err)
	}
	if !u.SendTo(serverAddr, request) {
		t.Fatal("SendTo failed")
	}
	buf := make([]byte, 8192)
	_ = fakeServer.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, _, err := fakeServer.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("fake server read: %v", err)
	}
	gotRequest := emuleClientUnwrap(t, buf[:n], baseKey)
	if !bytesEqual(gotRequest, request) {
		t.Fatalf("decrypted request differs: got % X want % X", gotRequest, request)
	}

	// 2) eMule-shaped encrypted OP_GLOBSEARCHRES answer (captured from
	//    Cyberchrist 18.1, one entry) must reach onSearch.
	searchHex := "e3993d54adcdbeaddeaa56654dfd078c0d42fb8d0600841a0500000082017a005562756e7475204c696e757820546f6f6c626f782d20313030302b20436f6d6d616e647320666f72205562756e747520616e642044656269616e20506f7765722055736572732c20326e642065642e202d204e65677573202857696c65793b323031333b393738313131383138333532363b656e67292e7064668302daa84700831502000000833002000000830305000000"
	searchPlain, err := hex.DecodeString(searchHex)
	if err != nil {
		t.Fatalf("decode search sample: %v", err)
	}
	if _, err := fakeServer.WriteToUDP(emuleServerWrap(t, searchPlain, baseKey, true), clientAddr); err != nil {
		t.Fatalf("send search answer: %v", err)
	}
	select {
	case got := <-searches:
		if len(got.entries) != 1 {
			t.Fatalf("search entries = %d, want 1", len(got.entries))
		}
		if got.entries[0].Hash.String() != "3D54ADCDBEADDEAA56654DFD078C0D42" {
			t.Fatalf("search hash = %s", got.entries[0].Hash.String())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("encrypted OP_GLOBSEARCHRES answer never reached the callback")
	}

	// 3) eMule-shaped encrypted OP_GLOBFOUNDSOURCES answer must reach onResults.
	sourcesHex := "e39bd6da273629b1c6781e96e0bab414260202041a01003612ffc005005a88"
	sourcesPlain, err := hex.DecodeString(sourcesHex)
	if err != nil {
		t.Fatalf("decode sources sample: %v", err)
	}
	if _, err := fakeServer.WriteToUDP(emuleServerWrap(t, sourcesPlain, baseKey, true), clientAddr); err != nil {
		t.Fatalf("send sources answer: %v", err)
	}
	select {
	case got := <-results:
		if got.hash.String() != "D6DA273629B1C6781E96E0BAB4142602" || len(got.endpoints) != 2 {
			t.Fatalf("sources = %s / %d", got.hash.String(), len(got.endpoints))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("encrypted OP_GLOBFOUNDSOURCES answer never reached the callback")
	}
}

// The key must be bound to the public IP reported by OP_IDCHANGE, not to a
// LOW_ID clientID. This is the bug fixed in the 20-byte login-answer path.
func TestSetServerUDPKeyUsesPublicIP(t *testing.T) {
	const (
		key      = uint32(0x0BADF00D)
		publicIP = uint32(0x3980A118) // 57.128.161.24
		lowID    = int32(0x01512345)
	)
	u := NewServerUDP()
	s := &Session{serverUDP: u, clientID: lowID, publicIP: publicIP}

	s.SetServerUDPKey(key, publicIP)
	if got := u.baseKey; got != key {
		t.Fatalf("valid key not installed: %#x", got)
	}

	u.SetUDPKey(0)
	s.SetServerUDPKey(key, uint32(lowID))
	if got := u.baseKey; got != 0 {
		t.Fatalf("key bound to LOW_ID clientID was installed: %#x", got)
	}

	u.SetUDPKey(0)
	s.SetServerUDPKey(key, publicIP^1)
	if got := u.baseKey; got != 0 {
		t.Fatalf("key bound to a foreign IP was installed: %#x", got)
	}

	u.SetUDPKey(0)
	s.publicIP = 0
	s.SetServerUDPKey(key, publicIP)
	if got := u.baseKey; got != 0 {
		t.Fatalf("key installed without a known public IP: %#x", got)
	}
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// OP_IDCHANGE's reported IP must flow all the way into the key validation
// state. A LOW_ID must not be mistaken for a public address.
func TestOnServerIDChangeStoresReportedPublicIP(t *testing.T) {
	const publicIP = uint32(0x3980A118)

	s := &Session{}
	sc := &ServerConnection{identifier: "fake:4661", session: s}
	s.OnServerIDChange(sc, 0x01512345, 0, 0, publicIP)
	if got := s.publicIP; got != publicIP {
		t.Fatalf("LOW_ID publicIP = %#x, want %#x", got, publicIP)
	}
	if got := s.clientID; got != 0x01512345 {
		t.Fatalf("clientID = %#x", got)
	}

	// HIGH_ID clients have no reported IP in older servers; their clientID is
	// already the public address.
	s2 := &Session{}
	sc2 := &ServerConnection{identifier: "fake2:4661", session: s2}
	s2.OnServerIDChange(sc2, int32(publicIP), 0, 0, 0)
	if got := s2.publicIP; got != publicIP {
		t.Fatalf("HIGH_ID publicIP = %#x, want %#x", got, publicIP)
	}
}

// A server that sends no ST_UDPKEY in OP_SERVERIDENT still hands out its key
// in OP_GLOBSERVSTATRES. Exercise that whole path over a real UDP socket:
// crypt ping -> challenge-keyed answer -> key installed -> later requests
// encrypted with the installed key.
func TestCryptPingInstallsKeyThenEncryptsRequests(t *testing.T) {
	fakeServer, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("fake server: %v", err)
	}
	defer fakeServer.Close()
	serverAddr := fakeServer.LocalAddr().(*net.UDPAddr)

	u := NewServerUDP()
	if err := u.Start(0); err != nil {
		t.Fatalf("ServerUDP.Start: %v", err)
	}
	defer u.Close()
	clientAddr := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: u.UDPPort()}
	if clientAddr.Port == 0 {
		t.Fatal("ServerUDP did not bind a port")
	}
	keys := make(chan uint32, 4)
	u.SetServerKeyHandler(func(key uint32) {
		u.SetUDPKey(key) // mirrors Session.SetServerUDPKey -> ServerUDP.SetUDPKey
		keys <- key
	})

	const challenge = uint32(0x12345678)
	const serverKey = uint32(0xA1B2C3D4)
	if !u.SendCryptPing(serverAddr, challenge, 5) {
		t.Fatal("SendCryptPing failed")
	}
	buf := make([]byte, 8192)
	_ = fakeServer.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, _, err := fakeServer.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("fake server read ping: %v", err)
	}
	// eMule sends the crypt-ping body in cleartext (raw packets are never
	// encrypted); only the answer is keyed by the challenge.
	ping := buf[:n]
	if len(ping) < 4 || binary.LittleEndian.Uint32(ping[:4]) != challenge {
		t.Fatalf("crypt ping body = % X, want challenge %#x", ping, challenge)
	}
	if len(ping) != 9 { // <challenge 4><padding 5>
		t.Fatalf("crypt ping body length = %d, want 9", len(ping))
	}

	// eMule OP_GLOBSERVSTATRES body: challenge/users/files/.../udpflags/
	// lowidusers/obfuscation ports/udpkey. The key lives at body[36:40].
	body := make([]byte, 40)
	binary.LittleEndian.PutUint32(body[0:4], challenge)
	binary.LittleEndian.PutUint32(body[4:8], 1234)      // users
	binary.LittleEndian.PutUint32(body[8:12], 5678)     // files
	binary.LittleEndian.PutUint32(body[24:28], 0x73b)   // udp flags
	binary.LittleEndian.PutUint16(body[32:34], 4674)    // udp obfuscation port
	binary.LittleEndian.PutUint16(body[34:36], 4670)    // tcp obfuscation port
	binary.LittleEndian.PutUint32(body[36:40], serverKey)
	answer := append([]byte{protocol.EdonkeyHeader, serverproto.OpGlobServStatRes}, body...)
	if _, err := fakeServer.WriteToUDP(emuleServerWrap(t, answer, challenge, true), clientAddr); err != nil {
		t.Fatalf("send crypt ping answer: %v", err)
	}
	select {
	case key := <-keys:
		if key != serverKey {
			t.Fatalf("installed key = %#x, want %#x", key, serverKey)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("OP_GLOBSERVSTATRES key never reached the handler")
	}

	// The installed key must now be used for ordinary datagrams.
	req := &serverproto.SearchRequest{Query: "debian"}
	request, err := serverproto.UDPGlobSearchDatagram(req, serverproto.UDPFlagExtGetFiles)
	if err != nil {
		t.Fatalf("pack request: %v", err)
	}
	if !u.SendTo(serverAddr, request) {
		t.Fatal("SendTo failed")
	}
	_ = fakeServer.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, _, err = fakeServer.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("fake server read request: %v", err)
	}
	got := emuleClientUnwrap(t, buf[:n], serverKey)
	if !bytesEqual(got, request) {
		t.Fatalf("post-key request = % X, want % X", got, request)
	}
}
