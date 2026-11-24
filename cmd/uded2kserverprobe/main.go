// Command uded2kserverprobe is an evidence-collection-only eD2k client probe
// for the WBS 16 ED-01/03/04 gates. It boots the fork engine with DHT disabled,
// loads a transfer for a known file hash, connects to a controlled eD2k server
// (cmd/uded2kfakeserver) and records: server IDClass (High/Low), handshake
// state, visible source count (policy size), active peer connections and any
// remote user hashes learned through the cleartext Hello exchange.
//
// It never touches ud state or the daemon. Run it on a lab host with a real
// network; stats are emitted as JSON for evidence.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	goed2k "github.com/asdfgh129/gobitmule/internal/ed2k"
	"github.com/asdfgh129/gobitmule/internal/ed2k/protocol"
)

type peerEvidence struct {
	Endpoint   string `json:"endpoint"`
	SourceFlag int    `json:"source_flag"`
	UserHash   string `json:"user_hash,omitempty"`
}

type serverEvidence struct {
	Identifier         string `json:"identifier"`
	ClientID           int32  `json:"client_id"`
	IDClass            string `json:"id_class"`
	HandshakeCompleted bool   `json:"handshake_completed"`
}

type report struct {
	Timestamp      string           `json:"timestamp"`
	Server         string           `json:"server"`
	UserHash       string           `json:"user_hash"`
	FileHash       string           `json:"file_hash"`
	ListenPort     int              `json:"listen_port"`
	Servers        []serverEvidence `json:"servers"`
	SourcesSent    int              `json:"sources_sent_expected"`
	VisibleSources int              `json:"visible_sources"`
	ActiveConns    int              `json:"active_connections"`
	Peers          []peerEvidence   `json:"peers,omitempty"`
	LearnedHashes  []string         `json:"learned_hashes,omitempty"`
	Verdict        string           `json:"verdict"`
	BlockReason    string           `json:"block_reason,omitempty"`
}

func main() {
	os.Exit(run())
}

func run() int {
	server := flag.String("server", "127.0.0.1:4712", "controlled eD2k server address")
	fileHash := flag.String("hash", "E75AD307F1EC36B45DD100AB92A2F8C0", "transfer file hash (32 hex)")
	size := flag.Int64("size", 1038612780, "transfer file size")
	userHash := flag.String("user-hash", "", "local eD2k UserHash (32 hex)")
	tcp := flag.Int("tcp", 0, "TCP listen port (0 = ephemeral)")
	duration := flag.Int("duration", 8, "seconds to observe after connect")
	sourcesExpected := flag.Int("sources-expected", 0, "number of sources the server advertised (for ratio evidence)")
	debug := flag.Bool("debug", false, "emit internal goed2k debug logs to stderr")
	flag.Parse()

	r := report{
		Timestamp:   time.Now().UTC().Format(time.RFC3339),
		Server:      *server,
		FileHash:    *fileHash,
		ListenPort:  *tcp,
		SourcesSent: *sourcesExpected,
		Verdict:     "NOT_EXECUTED",
	}
	if *debug {
		goed2k.SetDebugLog(os.Stderr)
	} else {
		goed2k.SetDebugLog(io.Discard)
	}

	st := goed2k.NewSettings()
	st.ListenPort = *tcp
	st.EnableDHT = false // server-driven gates; KAD is covered by ED-02
	st.MaxUploadRateKB = 0
	c := goed2k.NewClient(st)
	c.SetAutoSaveInterval(0)
	defer c.Close()

	if *userHash != "" {
		h, err := protocol.HashFromString(*userHash)
		if err != nil {
			r.BlockReason = "invalid -user-hash: " + err.Error()
			printJSON(r)
			return 2
		}
		if err := c.SetUserHash(h); err != nil {
			r.BlockReason = "set -user-hash failed: " + err.Error()
			printJSON(r)
			return 2
		}
	}
	r.UserHash = c.UserHash().String()

	h, err := protocol.HashFromString(*fileHash)
	if err != nil {
		r.BlockReason = "invalid -hash: " + err.Error()
		printJSON(r)
		return 2
	}
	tmp, err := os.CreateTemp(os.TempDir(), "ud-serverprobe-*.part")
	if err != nil {
		r.BlockReason = "temp file: " + err.Error()
		printJSON(r)
		return 2
	}
	defer func() { _ = tmp.Close(); _ = os.Remove(tmp.Name()) }()
	if _, err := c.AddTransfer(goed2k.NewAddTransferParamsFromFile(h, goed2k.CurrentTimeMillis(), *size, tmp, false)); err != nil {
		r.BlockReason = "AddTransfer: " + err.Error()
		printJSON(r)
		return 2
	}
	if err := c.Start(); err != nil {
		r.BlockReason = "client Start: " + err.Error()
		printJSON(r)
		return 2
	}
	if err := c.Connect(*server); err != nil {
		r.BlockReason = "server Connect: " + err.Error()
		printJSON(r)
		return 2
	}

	handle := c.FindTransfer(h)
	deadline := time.Now().Add(time.Duration(*duration) * time.Second)
	handshakeSeen := false
	for time.Now().Before(deadline) {
		for _, s := range c.ServerStatuses() {
			if !s.HandshakeCompleted {
				continue
			}
			handshakeSeen = true
			se := serverEvidence{Identifier: s.Identifier, ClientID: s.ClientID, IDClass: s.IDClass(), HandshakeCompleted: true}
			found := false
			for _, existing := range r.Servers {
				if existing.Identifier == se.Identifier {
					found = true
				}
			}
			if !found {
				r.Servers = append(r.Servers, se)
			}
		}
		if handle.IsValid() {
			status := handle.GetStatus()
			r.VisibleSources = status.NumPeers
			r.ActiveConns = handle.ActiveConnections()
			seen := map[string]bool{}
			for _, pi := range handle.GetPeersInfo() {
				hashStr := ""
				if !pi.UserHash.Equal(protocol.Invalid) {
					hashStr = pi.UserHash.String()
					all := map[string]bool{}
					for _, lh := range r.LearnedHashes {
						all[lh] = true
					}
					if !all[hashStr] {
						r.LearnedHashes = append(r.LearnedHashes, hashStr)
					}
				}
				key := fmt.Sprintf("%s|%d|%s", pi.Endpoint.String(), pi.SourceFlag, hashStr)
				if seen[key] {
					continue
				}
				seen[key] = true
				r.Peers = append(r.Peers, peerEvidence{Endpoint: pi.Endpoint.String(), SourceFlag: pi.SourceFlag, UserHash: hashStr})
			}
		}
		if handshakeSeen && r.VisibleSources >= *sourcesExpected && *sourcesExpected > 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	if len(r.Servers) == 0 || !r.Servers[0].HandshakeCompleted {
		r.Verdict = "FAIL"
		r.BlockReason = "server handshake did not complete"
		printJSON(r)
		return 2
	}
	r.Verdict = "PASS"
	printJSON(r)
	return 0
}

func printJSON(r report) {
	b, _ := json.MarshalIndent(r, "", "  ")
	fmt.Println(string(b))
}
