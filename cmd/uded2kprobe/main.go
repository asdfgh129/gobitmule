// Command uded2kprobe is the WBS 16 / ED-01/02/03/06 lab probe: it boots the
// forked goed2k engine (internal/ed2k) with a KAD nodes.dat, waits for
// bootstrap, optionally injects an extra KAD node (peer visibility), searches
// a file hash on KAD (source discovery) and records the SecureIdent user hash
// from a persistent client state file. It is evidence-collection only — it
// never writes ud state or touches the daemon. Run it on a lab host with real
// network.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"time"

	goed2k "github.com/asdfgh129/ud/internal/ed2k"
	"github.com/asdfgh129/ud/internal/ed2k/protocol"
	kadproto "github.com/asdfgh129/ud/internal/ed2k/protocol/kad"
)

type sourceEvidence struct {
	Endpoint string `json:"endpoint"`
	FileSize int64  `json:"file_size,omitempty"`
}

type report struct {
	Timestamp           string           `json:"timestamp"`
	NodesDat            string           `json:"nodes_dat"`
	StatePath           string           `json:"state_path,omitempty"`
	UserHash            string           `json:"user_hash"`
	SecureIdent         bool             `json:"secure_ident"`
	CryptLayerSupported bool             `json:"crypt_layer_supported"`
	AddedNode           string           `json:"added_node,omitempty"`
	SearchHash          string           `json:"search_hash,omitempty"`
	PublishedEndpoint   string           `json:"published_endpoint,omitempty"`
	ListenPort          int              `json:"listen_port"`
	UDPPort             int              `json:"udp_port"`
	Status              goed2k.DHTStatus `json:"dht_status"`
	Sources             []sourceEvidence `json:"sources,omitempty"`
	SourceCount         int              `json:"source_count"`
	SearchStarted       bool             `json:"search_started,omitempty"`
	SearchCompleted     bool             `json:"search_completed,omitempty"`
	Verdict             string           `json:"verdict"`
	BlockReason         string           `json:"block_reason,omitempty"`
}

func main() {
	os.Exit(run())
}

func run() int {
	nodes := flag.String("nodes", "nodes.dat", "KAD nodes.dat bootstrap file")
	addNode := flag.String("add-node", "", "extra KAD node to inject (host:port), e.g. peer eMule")
	searchHash := flag.String("search-hash", "", "file hash (32 hex) to search sources on KAD")
	size := flag.Int64("size", 0, "file size for source search (required with -search-hash)")
	publishSelf := flag.Bool("publish-self", false, "publish self as source of -search-hash before searching")
	selfIP := flag.String("self-ip", "", "IPv4 to publish with -publish-self")
	searchWait := flag.Int("search-wait", 30, "seconds to wait for KAD source search traversal")
	dhtTimeout := flag.Int("dht-timeout", 8, "KAD RPC timeout in seconds")
	debug := flag.Bool("debug", false, "emit internal goed2k debug logs to stderr")
	tcp := flag.Int("tcp", 4662, "TCP listen port")
	udp := flag.Int("udp", 4672, "KAD UDP port")
	wait := flag.Int("wait", 30, "seconds to wait for KAD bootstrap")
	statePath := flag.String("state", "", "goed2k client state file (persists UserHash/SecureIdent)")
	userHash := flag.String("user-hash", "", "eD2k UserHash (32 hex) to set before persisting state")
	secureIdent := flag.Bool("secure-ident", false, "record SecureIdent mode in the client state")
	flag.Parse()

	r := report{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		NodesDat:  *nodes,
		AddedNode: *addNode, SearchHash: *searchHash,
		ListenPort: *tcp, UDPPort: *udp,
	}
	var explicitHash protocol.Hash
	if *userHash != "" {
		parsed, err := protocol.HashFromString(*userHash)
		if err != nil {
			r.Verdict = "NOT_EXECUTED"
			r.BlockReason = "invalid -user-hash: " + err.Error()
			printJSON(r)
			return 2
		}
		explicitHash = parsed
	}

	if *debug {
		goed2k.SetDebugLog(os.Stderr)
	} else {
		goed2k.SetDebugLog(io.Discard)
	}

	st := goed2k.NewSettings()
	st.ListenPort = *tcp
	st.UDPPort = *udp
	st.EnableDHT = true
	st.DHTSearchTimeout = *dhtTimeout
	st.MaxUploadRateKB = 0
	c := goed2k.NewClient(st)
	c.SetAutoSaveInterval(0)
	defer c.Close()

	if *statePath != "" {
		c.SetStatePath(*statePath)
		r.StatePath = c.StatePath()
	}
	if *secureIdent {
		c.SetSecureIdent(true)
	}
	if *userHash != "" {
		if err := c.SetUserHash(explicitHash); err != nil {
			r.Verdict = "NOT_EXECUTED"
			r.BlockReason = "set -user-hash failed: " + err.Error()
			printJSON(r)
			return 2
		}
	}
	if *statePath != "" {
		if err := c.LoadState(""); err != nil {
			r.Verdict = "NOT_EXECUTED"
			r.BlockReason = "state load failed: " + err.Error()
			printJSON(r)
			return 2
		}
		// An explicit -user-hash wins over a previously persisted value.
		if *userHash != "" {
			if err := c.SetUserHash(explicitHash); err != nil {
				r.Verdict = "NOT_EXECUTED"
				r.BlockReason = "set -user-hash failed: " + err.Error()
				printJSON(r)
				return 2
			}
		}
	}
	r.UserHash = c.UserHash().String()
	r.SecureIdent = c.SecureIdent()
	r.CryptLayerSupported = goed2k.CryptLayerSupported()

	if err := c.LoadDHTNodesDat(*nodes); err != nil {
		r.Verdict = "NOT_EXECUTED"
		r.BlockReason = "nodes.dat load failed: " + err.Error()
		printJSON(r)
		return 2
	}
	if *addNode != "" {
		if err := c.AddDHTBootstrapNodes(*addNode); err != nil {
			r.Verdict = "NOT_EXECUTED"
			r.BlockReason = "add-node failed: " + err.Error()
			printJSON(r)
			return 2
		}
	}
	if err := c.Start(); err != nil {
		r.Verdict = "NOT_EXECUTED"
		r.BlockReason = "engine start failed: " + err.Error()
		printJSON(r)
		return 2
	}

	deadline := time.Now().Add(time.Duration(*wait) * time.Second)
	for time.Now().Before(deadline) {
		s := c.DHTStatus()
		if s.Bootstrapped && s.LiveNodes > 0 {
			break
		}
		time.Sleep(2 * time.Second)
	}
	r.Status = c.DHTStatus()
	if !r.Status.Bootstrapped || r.Status.LiveNodes <= 0 {
		r.Verdict = "FAIL"
		r.BlockReason = "KAD did not bootstrap (no live nodes within wait window)"
		printJSON(r)
		return 2
	}

	// KAD source discovery for a known file hash (ED-02 "能找到源").
	if *searchHash != "" {
		h, err := protocol.HashFromString(*searchHash)
		if err != nil {
			r.Verdict = "NOT_EXECUTED"
			r.BlockReason = "invalid hash: " + err.Error()
			printJSON(r)
			return 2
		}
		tracker := c.GetDHTTracker()
		if tracker == nil {
			r.Verdict = "NOT_EXECUTED"
			r.BlockReason = "DHT tracker unavailable"
			printJSON(r)
			return 2
		}
		if *publishSelf {
			if *size <= 0 || *selfIP == "" {
				r.Verdict = "NOT_EXECUTED"
				r.BlockReason = "publish-self requires -size>0 and -self-ip"
				printJSON(r)
				return 2
			}
			tcpAddr, err := net.ResolveTCPAddr("tcp", fmt.Sprintf("%s:%d", *selfIP, *tcp))
			if err != nil {
				r.Verdict = "NOT_EXECUTED"
				r.BlockReason = "invalid -self-ip: " + err.Error()
				printJSON(r)
				return 2
			}
			ep := protocol.EndpointFromInet(tcpAddr)
			r.PublishedEndpoint = ep.String()
			tracker.PublishSource(h, ep, *size)
			time.Sleep(2 * time.Second)
		}
		var mu sync.Mutex
		seen := map[string]sourceEvidence{}
		searchCompleted := false
		ok := tracker.SearchSources(h, *size, func(entries []kadproto.SearchEntry) {
			mu.Lock()
			defer mu.Unlock()
			searchCompleted = true
			for _, e := range entries {
				ep, found := e.SourceEndpoint()
				if !found || !ep.Defined() {
					continue
				}
				key := ep.String()
				if _, dup := seen[key]; dup {
					continue
				}
				seen[key] = sourceEvidence{Endpoint: key}
			}
		})
		r.SearchStarted = ok
		if ok {
			// allow async two-phase KAD traversal (find-sources -> search-sources)
			deadlineS := time.Now().Add(time.Duration(*searchWait) * time.Second)
			for time.Now().Before(deadlineS) {
				mu.Lock()
				done := searchCompleted
				mu.Unlock()
				if done {
					break
				}
				time.Sleep(2 * time.Second)
			}
		}
		mu.Lock()
		r.SearchCompleted = searchCompleted
		for _, v := range seen {
			r.Sources = append(r.Sources, v)
		}
		mu.Unlock()
		r.SourceCount = len(r.Sources)
	}

	// Refresh after search so RunningTraversals reflects the completed work.
	r.Status = c.DHTStatus()
	if r.Status.Bootstrapped && r.Status.LiveNodes > 0 {
		r.Verdict = "PASS"
	} else {
		r.Verdict = "FAIL"
	}
	printJSON(r)
	if r.Verdict == "PASS" {
		return 0
	}
	return 2
}

func printJSON(r report) {
	b, _ := json.MarshalIndent(r, "", "  ")
	fmt.Println(string(b))
}
