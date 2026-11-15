package interop

// Deterministic, CI-runnable regression for ADR-0021 path A: after a download
// completes, the handed-off background seeding session must actually serve
// real BitTorrent wire peers, and Transport.Close() must reclaim the listener
// and temporary data directory.
//
// This is deliberately NOT an L4 case. Both peers are in-process anacrolix
// clients on loopback with a self-hosted tracker, no packet capture and no
// isolated gigabit LAN. It does not change Registry(), does not add a case to
// dist/interop.json and never flips release_gate_met.

import (
	"bytes"
	"context"
	"fmt"
	"math/rand"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/asdfgh129/ud/internal/core"
	bttransport "github.com/asdfgh129/ud/internal/transport/bt"
)

func makeSeedingPayload(n int) []byte {
	p := make([]byte, n)
	r := rand.New(rand.NewSource(20261115))
	for i := range p {
		p[i] = byte(r.Intn(256))
	}
	return p
}

func freeTCPPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve a loopback port: %v", err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func TestPathASeedingServesPeerAfterRunReturns(t *testing.T) {
	content := makeSeedingPayload(256 * 1024)
	infoBytes, _, err := makeSingleFileInfo(content, "seeding.bin")
	if err != nil {
		t.Fatalf("build metainfo: %v", err)
	}

	tracker := NewHTTPTracker(false, WithStats(ScrapeStat{Complete: 1, Incomplete: 1}))
	defer tracker.Close()
	mi := &metainfo.MetaInfo{
		InfoBytes:    bencode.Bytes(infoBytes),
		Announce:     tracker.URL(),
		AnnounceList: metainfo.AnnounceList{{tracker.URL()}},
	}
	raw, err := bencode.Marshal(mi)
	if err != nil {
		t.Fatalf("marshal metainfo: %v", err)
	}

	seed, err := startSeeder(mi, map[string][]byte{"seeding.bin": content})
	if err != nil {
		t.Fatalf("start independent seeder: %v", err)
	}
	defer func() {
		if seed != nil {
			seed.Close()
		}
	}()
	tracker.SetPeers(CompactPeer{IP: net.ParseIP("127.0.0.1"), Port: portOf(seed.addr)})

	udPort := freeTCPPort(t)
	policy := bttransport.SeedingPolicy{
		Enabled:        true,
		SeedRatio:      0, // ratio target disabled: keep the session alive for this test
		SeedTime:       10 * time.Minute,
		MaxUploadSlots: 8,
		EnableUPnP:     false,
		EnableDHT:      false,
		EnablePEX:      false,
		PeerID:         "-UD0000-",
		ListenPort:     udPort,
		ListenHost:     "127.0.0.1",
		StatsInterval:  100 * time.Millisecond,
	}
	// Private=false is required by seedingApplicable(); DHT/PEX stay off via
	// the policy, so the loopback lab remains tracker-only.
	res := &core.Resource{
		Kind:     core.KindTorrent,
		Files:    []core.ResourceFile{{Path: "seeding.bin", Selected: true}},
		Identity: core.ProtocolIdentity{TorrentMeta: raw},
	}
	transport := bttransport.New(nil, noopCreds{},
		bttransport.WithBootstrapPeers(seed.addr),
		bttransport.WithPlainHandshake(),
		bttransport.WithSeedingPolicy(policy),
		bttransport.WithLifecycleContext(context.Background()),
	)
	defer transport.Close()

	cw := newChunkWriter(int64(len(content)), int64(len(content)))
	runCtx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := transport.Run(runCtx, res, cw, nil); err != nil {
		t.Fatalf("seeding transport download: %v", err)
	}
	if !bytes.Equal(cw.Bytes(), content) {
		t.Fatalf("downloaded bytes differ from seeder payload")
	}

	// Remove the ONLY other source of the payload. Any byte the second peer
	// receives now must come from ud's background seeding session.
	seed.Close()
	seed = nil
	udAddr := fmt.Sprintf("127.0.0.1:%d", udPort)
	tracker.SetPeers(CompactPeer{IP: net.ParseIP("127.0.0.1"), Port: udPort})
	if !waitFor(10*time.Second, func() bool {
		c, err := net.DialTimeout("tcp", udAddr, 500*time.Millisecond)
		if err == nil {
			_ = c.Close()
		}
		return err == nil
	}) {
		t.Fatalf("seeding transport never listened on %s", udAddr)
	}

	leecher := bttransport.New(nil, noopCreds{},
		bttransport.WithBootstrapPeers(&net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: udPort}),
		bttransport.WithPlainHandshake(),
	)
	// Leech-only: no WithSeedingPolicy, so this transport must NOT start a
	// second seeding session. Private=true keeps DHT/PEX off for the lab.
	leechRes := &core.Resource{
		Kind:     core.KindTorrent,
		Files:    []core.ResourceFile{{Path: "seeding.bin", Selected: true}},
		Identity: core.ProtocolIdentity{TorrentMeta: raw, Private: true},
	}
	cw2 := newChunkWriter(int64(len(content)), int64(len(content)))
	leachCtx, leachCancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer leachCancel()
	if err := leecher.Run(leachCtx, leechRes, cw2, nil); err != nil {
		t.Fatalf("leecher could not download from ud seeding session: %v", err)
	}
	if !bytes.Equal(cw2.Bytes(), content) {
		t.Fatalf("bytes served by ud seeding session differ from original payload")
	}

	honest := false
	for _, a := range tracker.Announces() {
		if strings.HasPrefix(a.PeerID, "-UD") {
			honest = true
			break
		}
	}
	if !honest {
		t.Fatalf("no honest -UD PeerID observed in tracker announces: %+v", tracker.Announces())
	}

	// Lifecycle: stopping the transport must release the listener and be safe
	// to call more than once.
	transport.Close()
	transport.Close()
	if !waitFor(5*time.Second, func() bool {
		c, err := net.DialTimeout("tcp", udAddr, 300*time.Millisecond)
		if err == nil {
			_ = c.Close()
			return false
		}
		return true
	}) {
		t.Fatalf("seeding listener %s still reachable after Transport.Close()", udAddr)
	}
}
