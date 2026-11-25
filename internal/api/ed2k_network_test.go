package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

type fakeED2KNetwork struct {
	servers             ED2KServersStatus
	kad                 ED2KKadStatus
	added               string
	removed             string
	node                string
	updatedFromPeers    bool
	bootstrappedKadPeer bool
}

func (f *fakeED2KNetwork) Servers(context.Context) (ED2KServersStatus, error) { return f.servers, nil }
func (f *fakeED2KNetwork) AddServer(_ context.Context, in ED2KServerInput) (ED2KServersStatus, error) {
	f.added = in.Address
	return f.servers, nil
}
func (f *fakeED2KNetwork) RemoveServer(_ context.Context, address string) (ED2KServersStatus, error) {
	f.removed = address
	return f.servers, nil
}
func (f *fakeED2KNetwork) ConnectServer(_ context.Context, address string) (ED2KServersStatus, error) {
	f.added = "connect:" + address
	return f.servers, nil
}
func (f *fakeED2KNetwork) DisconnectServers(context.Context) (ED2KServersStatus, error) {
	return f.servers, nil
}
func (f *fakeED2KNetwork) Kad(context.Context) (ED2KKadStatus, error) { return f.kad, nil }
func (f *fakeED2KNetwork) AddKadNode(_ context.Context, address string) (ED2KKadStatus, error) {
	f.node = address
	return f.kad, nil
}
func (f *fakeED2KNetwork) UpdateServersFromPeers(context.Context) (ED2KServersStatus, error) {
	f.updatedFromPeers = true
	return f.servers, nil
}
func (f *fakeED2KNetwork) BootstrapKadFromPeers(context.Context) (ED2KKadStatus, error) {
	f.bootstrappedKadPeer = true
	return f.kad, nil
}

func TestED2KNetworkDisabled(t *testing.T) {
	srv := httptest.NewServer(New(Options{}))
	defer srv.Close()
	r, err := http.Get(srv.URL + "/api/v1/ed2k/servers")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("GET servers without provider: status=%d, want 503", r.StatusCode)
	}
}

func TestED2KNetworkRoutes(t *testing.T) {
	fake := &fakeED2KNetwork{
		servers: ED2KServersStatus{Available: true, Servers: []ED2KServerEntry{}},
		kad:     ED2KKadStatus{Available: true, Nodes: []ED2KKadNode{}},
	}
	srv := httptest.NewServer(New(Options{Network: fake}))
	defer srv.Close()

	r, err := http.Get(srv.URL + "/api/v1/ed2k/servers")
	if err != nil {
		t.Fatal(err)
	}
	var got ED2KServersStatus
	if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if !got.Available || r.StatusCode != http.StatusOK {
		t.Fatalf("servers response = %+v status=%d", got, r.StatusCode)
	}

	add, err := http.Post(srv.URL+"/api/v1/ed2k/servers", "application/json",
		bytes.NewBufferString(`{"address":"1.2.3.4:4661","name":"test"}`))
	if err != nil {
		t.Fatal(err)
	}
	add.Body.Close()
	if add.StatusCode != http.StatusOK || fake.added != "1.2.3.4:4661" {
		t.Fatalf("add server: status=%d added=%q", add.StatusCode, fake.added)
	}

	node, err := http.Post(srv.URL+"/api/v1/ed2k/kad/nodes", "application/json",
		bytes.NewBufferString(`{"address":"5.6.7.8:4662"}`))
	if err != nil {
		t.Fatal(err)
	}
	node.Body.Close()
	if node.StatusCode != http.StatusOK || fake.node != "5.6.7.8:4662" {
		t.Fatalf("add kad node: status=%d node=%q", node.StatusCode, fake.node)
	}

	bad, err := http.Post(srv.URL+"/api/v1/ed2k/kad/nodes", "application/json",
		bytes.NewBufferString(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	bad.Body.Close()
	if bad.StatusCode != http.StatusBadRequest {
		t.Fatalf("empty node address: status=%d, want 400", bad.StatusCode)
	}

	updated, err := http.Post(srv.URL+"/api/v1/ed2k/servers/update-from-peers", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	updated.Body.Close()
	if updated.StatusCode != http.StatusOK || !fake.updatedFromPeers {
		t.Fatalf("update servers from peers: status=%d called=%v", updated.StatusCode, fake.updatedFromPeers)
	}

	bootstrapped, err := http.Post(srv.URL+"/api/v1/ed2k/kad/bootstrap-from-peers", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	bootstrapped.Body.Close()
	if bootstrapped.StatusCode != http.StatusOK || !fake.bootstrappedKadPeer {
		t.Fatalf("bootstrap kad from peers: status=%d called=%v", bootstrapped.StatusCode, fake.bootstrappedKadPeer)
	}
}
