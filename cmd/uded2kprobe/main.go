// Command uded2kprobe is the WBS 16 / ED-01/02/03 lab probe: it boots the
// forked goed2k engine (internal/ed2k) with a KAD nodes.dat, waits for
// bootstrap, optionally injects an extra KAD node (peer visibility) and
// searches a file hash on KAD (source discovery), then reports evidences as
// JSON. It is evidence-collection only — it never writes ud state or touches
// the daemon. Run it on a lab host with real network.
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
	Timestamp         string           `json:"timestamp"`
	NodesDat          string           `json:"nodes_dat"`
	AddedNode         string           `json:"added_node,omitempty"`
	SearchHash        string           `json:"search_hash,omitempty"`
	PublishedEndpoint string           `json:"published_endpoint,omitempty"`
	ListenPort        int              `json:"listen_port"`
	UDPPort           int              `json:"udp_port"`
	Status            goed2k.DHTStatus `json:"dht_status"`
	Sources           []sourceEvidence `json:"sources,omitempty"`
	SourceCount       int              `json:"source_count"`
	SearchStarted     bool             `json:"search_started,omitempty"`
	SearchCompleted   bool             `json:"search_completed,omitempty"`
	Verdict           string           `json:"verdict"`
	BlockReason       string           `json:"block_reason,omitempty"`
}

func main() {
	nodes := flag.String("nodes", "nodes.dat", "KAD nodes.dat bootstrap file")
	addNode := flag.String("add-node", "", "extra KAD node to inject (host:port), e.g. peer eMule")
	searchHash := flag.String("search-hash", "", "file hash (32 hex) to search sources on KAD")
	size := flag.Int64("size", 0, "file size for source search (required with -search-hash)")
	publishSelf := flag.Bool("publish-self", false, "publish self as source of -search-hash before searching")
	selfIP := flag.String("self-ip", "", "IPv4 to publish with -publish-self")
	searchWait := flag.Int("search-wait", 30, "seconds to wait for KAD source search traversal")
	debug := flag.Bool("debug", false, "emit internal goed2k debug logs to stderr")
	tcp := flag.Int("tcp", 4662, "TCP listen port")
	udp := flag.Int("udp", 4672, "KAD UDP port")
	wait := flag.Int("wait", 30, "seconds to wait for KAD bootstrap")
	flag.Parse()

	if *debug {
		goed2k.SetDebugLog(os.Stderr)
	} else {
		goed2k.SetDebugLog(io.Discard)
	}

	st := goed2k.NewSettings()
	st.ListenPort = *tcp
	st.UDPPort = *udp
	st.EnableDHT = true
	st.MaxUploadRateKB = 0
	c := goed2k.NewClient(st)
	c.SetAutoSaveInterval(0)

	r := report{Timestamp: time.Now().UTC().Format(time.RFC3339), NodesDat: *nodes,
		AddedNode: *addNode, SearchHash: *searchHash, ListenPort: *tcp, UDPPort: *udp}

	if err := c.LoadDHTNodesDat(*nodes); err != nil {
		r.Verdict = "NOT_EXECUTED"
		r.BlockReason = "nodes.dat load failed: " + err.Error()
		printJSON(r)
		return
	}
	if *addNode != "" {
		if err := c.AddDHTBootstrapNodes(*addNode); err != nil {
			r.Verdict = "NOT_EXECUTED"
			r.BlockReason = "add-node failed: " + err.Error()
			printJSON(r)
			return
		}
	}
	if err := c.Start(); err != nil {
		r.Verdict = "NOT_EXECUTED"
		r.BlockReason = "engine start failed: " + err.Error()
		printJSON(r)
		return
	}
	defer c.Close()

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
		return
	}

	// KAD source discovery for a known file hash (ED-02 "能找到源").
	if *searchHash != "" {
		h, err := protocol.HashFromString(*searchHash)
		if err != nil {
			r.Verdict = "NOT_EXECUTED"
			r.BlockReason = "invalid hash: " + err.Error()
			printJSON(r)
			return
		}
		tracker := c.GetDHTTracker()
		if tracker == nil {
			r.Verdict = "NOT_EXECUTED"
			r.BlockReason = "DHT tracker unavailable"
			printJSON(r)
			return
		}
		if *publishSelf {
			if *size <= 0 || *selfIP == "" {
				r.Verdict = "NOT_EXECUTED"
				r.BlockReason = "publish-self requires -size>0 and -self-ip"
				printJSON(r)
				return
			}
			tcpAddr, err := net.ResolveTCPAddr("tcp", fmt.Sprintf("%s:%d", *selfIP, *tcp))
			if err != nil {
				r.Verdict = "NOT_EXECUTED"
				r.BlockReason = "invalid -self-ip: " + err.Error()
				printJSON(r)
				return
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

	if r.Status.Bootstrapped && r.Status.LiveNodes > 0 {
		r.Verdict = "PASS"
	} else {
		r.Verdict = "FAIL"
	}
	printJSON(r)
}

func printJSON(r report) {
	b, _ := json.MarshalIndent(r, "", "  ")
	fmt.Println(string(b))
	if r.Verdict == "NOT_EXECUTED" || r.Verdict == "FAIL" {
		os.Exit(2)
	}
}
