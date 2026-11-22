// Command uded2kprobe is the WBS 16 / ED-02/03 lab probe: it boots the forked
// goed2k engine (internal/ed2k) with a KAD nodes.dat, waits for bootstrap, and
// reports DHTStatus as JSON. It is evidence-collection only — it never writes
// ud state or touches the daemon. Run it on a lab host with real network.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	goed2k "github.com/asdfgh129/ud/internal/ed2k"
)

type report struct {
	Timestamp   string           `json:"timestamp"`
	NodesDat    string           `json:"nodes_dat"`
	ListenPort  int              `json:"listen_port"`
	UDPPort     int              `json:"udp_port"`
	Status      goed2k.DHTStatus `json:"dht_status"`
	Verdict     string           `json:"verdict"`
	BlockReason string           `json:"block_reason,omitempty"`
}

func main() {
	nodes := flag.String("nodes", "nodes.dat", "KAD nodes.dat bootstrap file")
	tcp := flag.Int("tcp", 4662, "TCP listen port")
	udp := flag.Int("udp", 4672, "KAD UDP port")
	wait := flag.Int("wait", 30, "seconds to wait for KAD bootstrap")
	flag.Parse()

	st := goed2k.NewSettings()
	st.ListenPort = *tcp
	st.UDPPort = *udp
	st.EnableDHT = true
	st.MaxUploadRateKB = 0
	c := goed2k.NewClient(st)
	c.SetAutoSaveInterval(0)

	r := report{Timestamp: time.Now().UTC().Format(time.RFC3339), NodesDat: *nodes,
		ListenPort: *tcp, UDPPort: *udp}

	if err := c.LoadDHTNodesDat(*nodes); err != nil {
		r.Verdict = "NOT_EXECUTED"
		r.BlockReason = "nodes.dat load failed: " + err.Error()
		printJSON(r)
		return
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
	if r.Status.Bootstrapped && r.Status.LiveNodes > 0 {
		r.Verdict = "PASS"
	} else {
		r.Verdict = "FAIL"
		r.BlockReason = "KAD did not bootstrap (no live nodes within wait window)"
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
