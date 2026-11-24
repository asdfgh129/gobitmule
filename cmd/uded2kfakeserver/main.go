// Command uded2kfakeserver is an evidence-collection-only controlled eD2k
// server used by the WBS 16 ED-01/03/04 gates. It speaks the fork's *exact*
// eD2k server wire codec (serverproto.PacketCombiner) over a real TCP socket:
//   - accepts LoginRequest (0x01), records the client's user hash / endpoint
//   - replies IdChange (0x40) with a configurable High/Low client ID
//   - accepts GetFileSources (0x19), replies FoundFileSources (0x42) with a
//     configurable High/Low source mix, OR (broker mode) advertises every
//     other connected client as a source so two clients dial each other.
//
// It never touches ud state or the daemon. Run it on a lab host with a real
// network; stats are emitted as JSON for evidence.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"time"

	"github.com/asdfgh129/gobitmule/internal/ed2k/protocol"
	serverproto "github.com/asdfgh129/gobitmule/internal/ed2k/protocol/server"
)

type clientStat struct {
	UserHash         string `json:"user_hash"`
	Remote           string `json:"remote"`
	ListenPort       int    `json:"listen_port"`
	Login            bool   `json:"login"`
	SourcesRequested bool   `json:"sources_requested"`
}

type stats struct {
	Timestamp        string       `json:"timestamp"`
	Addr             string       `json:"addr"`
	ClientID         int32        `json:"client_id"`
	IDClass          string       `json:"id_class"`
	AnnouncedSources int          `json:"announced_sources"`
	AnnouncedHigh    int          `json:"announced_high"`
	AnnouncedLow     int          `json:"announced_low"`
	Broker           bool         `json:"broker"`
	LoginCount       int          `json:"login_count"`
	IDChangeSent     int          `json:"idchange_sent"`
	GetSourcesCount  int          `json:"getsources_count"`
	FoundSourcesSent int          `json:"found_sources_sent"`
	Clients          []clientStat `json:"clients"`
	Verdict          string       `json:"verdict"`
}

type clientConn struct {
	conn           net.Conn
	userHash       string
	remoteAddr     *net.TCPAddr
	listenPort     int
	requestedHash  protocol.Hash
	login          bool
	sourcesRequest bool
	foundSent      int
}

func isLowID(ip int32) bool {
	return int64(uint32(ip)) < 16777216 // HighestLowIDED2K
}

func readFrame(conn net.Conn) ([]byte, error) {
	header := make([]byte, protocol.PacketHeaderSize)
	if _, err := io.ReadFull(conn, header); err != nil {
		return nil, err
	}
	var parsed protocol.PacketHeader
	if err := parsed.Get(bytes.NewReader(header)); err != nil {
		return nil, err
	}
	bodyLen := int(parsed.SizePacket())
	if bodyLen < 0 || bodyLen > 1<<20 {
		return nil, io.ErrUnexpectedEOF
	}
	frame := make([]byte, protocol.PacketHeaderSize+bodyLen)
	copy(frame, header)
	if _, err := io.ReadFull(conn, frame[protocol.PacketHeaderSize:]); err != nil {
		return nil, err
	}
	return frame, nil
}

func main() {
	os.Exit(run())
}

func run() int {
	addr := flag.String("addr", "127.0.0.1:4712", "TCP listen address for the controlled server")
	clientID := flag.Int64("client-id", 0x70000001, "ClientID to grant in IdChange (default 0x70000001 = High ID)")
	idClass := flag.String("id-class", "", "override client-id by class: high=0x70000001, low=123456")
	sources := flag.Int("sources", 6, "number of sources to advertise in FoundFileSources")
	highSources := flag.Int("high-sources", -1, "how many advertised sources are High ID (default: all; rest are Low ID)")
	advertise := flag.String("advertise", "", "comma-separated host:port static source list (overrides sources/high-sources)")
	broker := flag.Bool("broker", false, "advertise every other connected client as a source (for mutual peer dial)")
	fileHash := flag.String("hash", "E75AD307F1EC36B45DD100AB92A2F8C0", "file hash carried by FoundFileSources")
	duration := flag.Int("duration", 8, "seconds to serve before emitting stats")
	statsOut := flag.String("stats-out", "", "optional path to write stats JSON")
	debug := flag.Bool("debug", false, "log each frame to stderr")
	flag.Parse()

	cid := int32(*clientID)
	switch *idClass {
	case "high":
		cid = 0x70000001
	case "low":
		cid = 123456
	case "":
	default:
		fmt.Fprintf(os.Stderr, "invalid -id-class %q\n", *idClass)
		return 2
	}

	hash, err := protocol.HashFromString(*fileHash)
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid -hash: %v\n", err)
		return 2
	}

	st := stats{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Addr:      *addr,
		ClientID:  cid,
		IDClass:   "HIGH_ID",
		Broker:    *broker,
		Verdict:   "NOT_EXECUTED",
	}
	if isLowID(int32(uint32(cid))) {
		st.IDClass = "LOW_ID"
	}

	// Build the advertised source set.
	high := *highSources
	if high < 0 {
		high = *sources
	}
	if high > *sources {
		high = *sources
	}
	low := *sources - high
	staticSources := make([]protocol.Endpoint, 0, *sources)
	if *advertise != "" {
		for _, spec := range splitCSV(*advertise) {
			host, portStr, err := net.SplitHostPort(spec)
			if err != nil {
				fmt.Fprintf(os.Stderr, "invalid -advertise entry %q: %v\n", spec, err)
				return 2
			}
			var port int
			fmt.Sscanf(portStr, "%d", &port)
			ep, err := protocol.EndpointFromString(host, port)
			if err != nil {
				fmt.Fprintf(os.Stderr, "invalid -advertise host %q: %v\n", host, err)
				return 2
			}
			staticSources = append(staticSources, ep)
		}
		high = 0
		low = 0
		for _, ep := range staticSources {
			if isLowID(ep.IP()) {
				low++
			} else {
				high++
			}
		}
	} else {
		for i := 0; i < high; i++ {
			staticSources = append(staticSources, protocol.NewEndpoint(int32(0x75010001+uint32(i)), 4662+i))
		}
		for i := 0; i < low; i++ {
			staticSources = append(staticSources, protocol.NewEndpoint(int32(0x000A0001+uint32(i)), 4662+i))
		}
	}
	st.AnnouncedSources = len(staticSources)
	st.AnnouncedHigh = high
	st.AnnouncedLow = low

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "listen: %v\n", err)
		return 2
	}
	defer ln.Close()

	combiner := serverproto.NewPacketCombiner()
	var mu sync.Mutex
	clients := map[net.Conn]*clientConn{}
	allClients := []*clientConn{} // retained for end-of-run stats even after disconnect
	stop := make(chan struct{})

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				select {
				case <-stop:
					return
				default:
					continue
				}
			}
			cc := &clientConn{conn: conn}
			mu.Lock()
			clients[conn] = cc
			allClients = append(allClients, cc)
			mu.Unlock()
			go handleClient(conn, cc, combiner, hash, cid, staticSources, *broker, *debug, &mu, clients)
		}
	}()

	// Broker mode: periodically advertise every other client as a source so
	// mutual dialing converges even when client start times are staggered.
	if *broker {
		go func() {
			ticker := time.NewTicker(500 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-stop:
					return
				case <-ticker.C:
					mu.Lock()
					for target, tc := range clients {
						if !tc.sourcesRequest {
							continue
						}
						peers := make([]protocol.Endpoint, 0, len(clients)-1)
						for other, oc := range clients {
							if other == target || oc.listenPort == 0 || oc.remoteAddr == nil {
								continue
							}
							ipv4 := oc.remoteAddr.IP.To4()
							if ipv4 == nil {
								continue
							}
							peers = append(peers, protocol.EndpointFromInet(&net.TCPAddr{IP: ipv4, Port: oc.listenPort}))
						}
						if len(peers) == 0 {
							continue
						}
						raw, err := combiner.Pack("server.FoundFileSources", &serverproto.FoundFileSources{Hash: tc.requestedHash, Sources: peers})
						if err != nil {
							continue
						}
						if _, err := target.Write(raw); err == nil {
							tc.foundSent++
						}
					}
					mu.Unlock()
				}
			}
		}()
	}

	time.Sleep(time.Duration(*duration) * time.Second)
	close(stop)
	mu.Lock()
	for _, cc := range allClients {
		remote := ""
		if cc.remoteAddr != nil {
			remote = cc.remoteAddr.String()
		}
		cs := clientStat{
			UserHash:         cc.userHash,
			Remote:           remote,
			ListenPort:       cc.listenPort,
			Login:            cc.login,
			SourcesRequested: cc.sourcesRequest,
		}
		st.Clients = append(st.Clients, cs)
		if cc.login {
			st.LoginCount++
		}
		if cc.sourcesRequest {
			st.GetSourcesCount++
		}
		st.FoundSourcesSent += cc.foundSent
	}
	mu.Unlock()
	st.IDChangeSent = st.LoginCount // one IdChange is sent per LoginRequest
	if st.LoginCount > 0 {
		st.Verdict = "PASS"
	}
	emitJSON(st, *statsOut)
	if st.Verdict == "PASS" {
		return 0
	}
	return 2
}

func handleClient(conn net.Conn, cc *clientConn, combiner protocol.PacketCombiner, hash protocol.Hash, cid int32, sources []protocol.Endpoint, broker, debug bool, mu *sync.Mutex, clients map[net.Conn]*clientConn) {
	defer func() {
		_ = conn.Close()
		mu.Lock()
		delete(clients, conn)
		mu.Unlock()
	}()
	_ = conn.SetDeadline(time.Now().Add(60 * time.Second))
	remote, _ := conn.RemoteAddr().(*net.TCPAddr)
	cc.remoteAddr = remote

	// 1) read LoginRequest, reply IdChange.
	frame, err := readFrame(conn)
	if err != nil {
		return
	}
	_, packet, err := combiner.UnpackFrame(frame)
	if err != nil {
		return
	}
	if debug {
		fmt.Fprintf(os.Stderr, "[%s] <- %T\n", remote, packet)
	}
	if login, ok := packet.(*serverproto.LoginRequest); ok {
		cc.userHash = login.Hash.String()
		cc.listenPort = login.Point.Port()
		cc.login = true
	}
	idRaw, err := combiner.Pack("server.IdChange", &serverproto.IdChange{ClientID: cid, TCPFlags: 0, AuxPort: 0})
	if err != nil {
		return
	}
	if _, err := conn.Write(idRaw); err != nil {
		return
	}
	if debug {
		fmt.Fprintf(os.Stderr, "[%s] -> IdChange clientID=%d\n", remote, cid)
	}

	// 2) read frames until GetFileSources, then reply FoundFileSources.
	for {
		frame, err := readFrame(conn)
		if err != nil {
			return
		}
		_, packet, err := combiner.UnpackFrame(frame)
		if err != nil {
			return
		}
		if debug {
			fmt.Fprintf(os.Stderr, "[%s] <- %T\n", remote, packet)
		}
		if req, ok := packet.(*serverproto.GetFileSources); ok {
			cc.requestedHash = req.Hash
			cc.sourcesRequest = true
			if !broker && len(sources) > 0 {
				srcRaw, err := combiner.Pack("server.FoundFileSources", &serverproto.FoundFileSources{Hash: req.Hash, Sources: sources})
				if err != nil {
					return
				}
				if _, err := conn.Write(srcRaw); err != nil {
					return
				}
				mu.Lock()
				cc.foundSent++
				mu.Unlock()
				if debug {
					fmt.Fprintf(os.Stderr, "[%s] -> FoundFileSources count=%d\n", remote, len(sources))
				}
			}
		}
	}
}

func splitCSV(s string) []string {
	out := []string{}
	for _, part := range bytes.Split([]byte(s), []byte{','}) {
		v := string(bytes.TrimSpace(part))
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

func emitJSON(st stats, path string) {
	b, _ := json.MarshalIndent(st, "", "  ")
	fmt.Println(string(b))
	if path != "" {
		_ = os.WriteFile(path, append(b, '\n'), 0644)
	}
}
