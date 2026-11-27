// Server list / KAD node wire surface for the Web console. DTOs here stay
// engine-agnostic; the daemon adapter owns the forked goed2k client.
package api

import (
	"context"
	"net/http"
	"strings"

	"github.com/asdfgh129/gobitmule/internal/core"
)

type ED2KServerEntry struct {
	Address           string `json:"address"`
	Name              string `json:"name"`
	Description       string `json:"description"`
	Source            string `json:"source"`
	Connected         bool   `json:"connected"`
	HandshakeComplete bool   `json:"handshakeComplete"`
	ClientID          int32  `json:"clientId"`
	// #16: latest server banner and user/file counts.
	LastMessage string `json:"lastMessage,omitempty"`
	UsersCount  int32  `json:"usersCount,omitempty"`
	FilesCount  int32  `json:"filesCount,omitempty"`
}

type ED2KServersStatus struct {
	Available     bool              `json:"available"`
	EngineStarted bool              `json:"engineStarted"`
	ServerMet     string            `json:"serverMet"`
	Connected     string            `json:"connected"`
	Servers       []ED2KServerEntry `json:"servers"`
	Message       string            `json:"message"`
}

type ED2KKadNode struct {
	ID        string `json:"id"`
	Address   string `json:"address"`
	TCPPort   int    `json:"tcpPort"`
	Version   int    `json:"version"`
	Seed      bool   `json:"seed"`
	HelloSent bool   `json:"helloSent"`
	Pinged    bool   `json:"pinged"`
	Responded bool   `json:"responded"`
	FailCount int    `json:"failCount"`
	FirstSeen int64  `json:"firstSeen"`
	LastSeen  int64  `json:"lastSeen"`
	Router    bool   `json:"router"`
}

type ED2KKadStatus struct {
	Available         bool          `json:"available"`
	EngineStarted     bool          `json:"engineStarted"`
	Bootstrapped      bool          `json:"bootstrapped"`
	Firewalled        bool          `json:"firewalled"`
	LiveNodes         int           `json:"liveNodes"`
	ReplacementNodes  int           `json:"replacementNodes"`
	RouterNodes       int           `json:"routerNodes"`
	RunningTraversals int           `json:"runningTraversals"`
	KnownNodes        int           `json:"knownNodes"`
	VerifiedNodes     int           `json:"verifiedNodes"`
	ListenPort        int           `json:"listenPort"`
	StoragePoint      string        `json:"storagePoint"`
	SelfID            string        `json:"selfId"`
	Nodes             []ED2KKadNode `json:"nodes"`
	Message           string        `json:"message"`
}

type ED2KServerInput struct {
	Address     string `json:"address"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

type ED2KNodeInput struct {
	Address string `json:"address"`
}

// ED2KNetworkProvider is implemented by the daemon layer. Nil means the
// capability is not wired in; unavailable engines reply with a clear Message.
type ED2KNetworkProvider interface {
	Servers(ctx context.Context) (ED2KServersStatus, error)
	AddServer(ctx context.Context, in ED2KServerInput) (ED2KServersStatus, error)
	RemoveServer(ctx context.Context, address string) (ED2KServersStatus, error)
	ConnectServer(ctx context.Context, address string) (ED2KServersStatus, error)
	DisconnectServers(ctx context.Context) (ED2KServersStatus, error)
	Kad(ctx context.Context) (ED2KKadStatus, error)
	AddKadNode(ctx context.Context, address string) (ED2KKadStatus, error)
	// UpdateServersFromPeers folds server endpoints learned from connected
	// clients into server.met (eMule: "Update server list when a client
	// connects"), exposed as an explicit action for the Web console.
	UpdateServersFromPeers(ctx context.Context) (ED2KServersStatus, error)
	// BootstrapKadFromPeers probes the Kad UDP ports learned from connected
	// clients, the eMule "Bootstrap" action on a client in the peer list.
	BootstrapKadFromPeers(ctx context.Context) (ED2KKadStatus, error)
}

func (s *Server) requireNetwork(w http.ResponseWriter) bool {
	if s.network == nil {
		writeError(w, core.Err(core.E_CAPABILITY_UNAVAILABLE, "eD2k network console is disabled"))
		return false
	}
	return true
}

func (s *Server) ed2kNetworkResult(w http.ResponseWriter, result any, err error) {
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) getED2KServers(w http.ResponseWriter, r *http.Request) {
	if !s.requireNetwork(w) {
		return
	}
	result, err := s.network.Servers(r.Context())
	s.ed2kNetworkResult(w, result, err)
}

func (s *Server) addED2KServer(w http.ResponseWriter, r *http.Request) {
	if !s.requireNetwork(w) {
		return
	}
	var in ED2KServerInput
	if err := decode(r, &in); err != nil {
		writeError(w, err)
		return
	}
	if strings.TrimSpace(in.Address) == "" {
		writeError(w, core.Err(core.E_INVALID_INPUT, "server address is required"))
		return
	}
	result, err := s.network.AddServer(r.Context(), in)
	s.ed2kNetworkResult(w, result, err)
}

func (s *Server) removeED2KServer(w http.ResponseWriter, r *http.Request) {
	if !s.requireNetwork(w) {
		return
	}
	address := strings.TrimSpace(r.URL.Query().Get("address"))
	if address == "" {
		writeError(w, core.Err(core.E_INVALID_INPUT, "server address is required"))
		return
	}
	result, err := s.network.RemoveServer(r.Context(), address)
	s.ed2kNetworkResult(w, result, err)
}

func (s *Server) connectED2KServer(w http.ResponseWriter, r *http.Request) {
	if !s.requireNetwork(w) {
		return
	}
	var in ED2KServerInput
	if err := decode(r, &in); err != nil {
		writeError(w, err)
		return
	}
	if strings.TrimSpace(in.Address) == "" {
		writeError(w, core.Err(core.E_INVALID_INPUT, "server address is required"))
		return
	}
	result, err := s.network.ConnectServer(r.Context(), in.Address)
	s.ed2kNetworkResult(w, result, err)
}

func (s *Server) disconnectED2KServers(w http.ResponseWriter, r *http.Request) {
	if !s.requireNetwork(w) {
		return
	}
	result, err := s.network.DisconnectServers(r.Context())
	s.ed2kNetworkResult(w, result, err)
}

func (s *Server) getED2KKad(w http.ResponseWriter, r *http.Request) {
	if !s.requireNetwork(w) {
		return
	}
	result, err := s.network.Kad(r.Context())
	s.ed2kNetworkResult(w, result, err)
}

func (s *Server) updateServersFromPeers(w http.ResponseWriter, r *http.Request) {
	if !s.requireNetwork(w) {
		return
	}
	result, err := s.network.UpdateServersFromPeers(r.Context())
	s.ed2kNetworkResult(w, result, err)
}

func (s *Server) bootstrapKadFromPeers(w http.ResponseWriter, r *http.Request) {
	if !s.requireNetwork(w) {
		return
	}
	result, err := s.network.BootstrapKadFromPeers(r.Context())
	s.ed2kNetworkResult(w, result, err)
}

func (s *Server) addED2KKadNode(w http.ResponseWriter, r *http.Request) {
	if !s.requireNetwork(w) {
		return
	}
	var in ED2KNodeInput
	if err := decode(r, &in); err != nil {
		writeError(w, err)
		return
	}
	if strings.TrimSpace(in.Address) == "" {
		writeError(w, core.Err(core.E_INVALID_INPUT, "node address is required"))
		return
	}
	result, err := s.network.AddKadNode(r.Context(), in.Address)
	s.ed2kNetworkResult(w, result, err)
}
