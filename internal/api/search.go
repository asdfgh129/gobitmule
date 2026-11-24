// eD2k search wire surface for the Web console. The API package stays
// transport-only: it defines a narrow SearchProvider interface and DTOs, while
// the daemon wires the forked goed2k engine behind it (docs/05 §1, ADR-0019).
package api

import (
	"context"
	"net/http"
	"strings"

	"github.com/asdfgh129/gobitmule/internal/core"
)

// ED2KSearchScope selects which eD2k backends take part in a keyword search.
// "all" is the default when the field is absent.
type ED2KSearchScope string

const (
	ED2KScopeAll    ED2KSearchScope = "all"
	ED2KScopeServer ED2KSearchScope = "server"
	ED2KScopeKAD    ED2KSearchScope = "kad"
)

// ED2KSearchState mirrors the engine's search lifecycle in wire-friendly form.
type ED2KSearchState string

const (
	ED2KSearchIdle     ED2KSearchState = "IDLE"
	ED2KSearchRunning  ED2KSearchState = "RUNNING"
	ED2KSearchFinished ED2KSearchState = "FINISHED"
	ED2KSearchStopped  ED2KSearchState = "STOPPED"
	ED2KSearchFailed   ED2KSearchState = "FAILED"
)

type ED2KSearchParams struct {
	Query              string          `json:"query"`
	Scope              ED2KSearchScope `json:"scope"`
	MinSize            int64           `json:"minSize"`
	MaxSize            int64           `json:"maxSize"`
	MinSources         int             `json:"minSources"`
	MinCompleteSources int             `json:"minCompleteSources"`
	FileType           string          `json:"fileType"`
	Extension          string          `json:"extension"`
	FileTypes          []string        `json:"fileTypes"`
	Sources            []string        `json:"sources"`
	Extensions         []string        `json:"extensions"`
}

// ED2KSearchResultSource is a bitmask-like label: a hash found through both
// backends is reported as "both".
type ED2KSearchResultSource string

const (
	ED2KResultServer ED2KSearchResultSource = "server"
	ED2KResultKAD    ED2KSearchResultSource = "kad"
	ED2KResultBoth   ED2KSearchResultSource = "both"
)

type ED2KSearchResult struct {
	Hash            string                 `json:"hash"`
	FileName        string                 `json:"fileName"`
	FileSize        int64                  `json:"fileSize"`
	Sources         int                    `json:"sources"`
	CompleteSources int                    `json:"completeSources"`
	MediaBitrate    int                    `json:"mediaBitrate"`
	MediaLength     int                    `json:"mediaLength"`
	MediaCodec      string                 `json:"mediaCodec"`
	Extension       string                 `json:"extension"`
	FileType        string                 `json:"fileType"`
	Source          ED2KSearchResultSource `json:"source"`
	ED2KLink        string                 `json:"ed2kLink"`
}

type ED2KSearchSnapshot struct {
	ID         uint32             `json:"id"`
	Params     ED2KSearchParams   `json:"params"`
	State      ED2KSearchState    `json:"state"`
	Results    []ED2KSearchResult `json:"results"`
	StartedAt  int64              `json:"startedAt"`
	UpdatedAt  int64              `json:"updatedAt"`
	ServerBusy bool               `json:"serverBusy"`
	DHTBusy    bool               `json:"dhtBusy"`
	KadKeyword string             `json:"kadKeyword"`
	Error      string             `json:"error"`
}

// ED2KSearchStatus is the common envelope for start/status/stop. Available is
// false when the daemon has no eD2k engine (ed2k.enabled=false); the UI uses it
// to explain the empty state instead of presenting a broken search box.
type ED2KSearchStatus struct {
	Available bool               `json:"available"`
	Snapshot  ED2KSearchSnapshot `json:"snapshot"`
}

// ED2KSearchProvider is implemented by the daemon layer, not by the API.
// A nil provider means the capability is not compiled/configured in.
type ED2KSearchProvider interface {
	Start(ctx context.Context, params ED2KSearchParams) (ED2KSearchStatus, error)
	Snapshot(ctx context.Context) (ED2KSearchStatus, error)
	Stop(ctx context.Context) (ED2KSearchStatus, error)
}

func (s *Server) ed2kStatus(w http.ResponseWriter, status ED2KSearchStatus, err error) {
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, status)
}

func (s *Server) startED2KSearch(w http.ResponseWriter, r *http.Request) {
	if s.search == nil {
		writeError(w, core.Err(core.E_CAPABILITY_UNAVAILABLE, "eD2k search is disabled"))
		return
	}
	var params ED2KSearchParams
	if err := decode(r, &params); err != nil {
		writeError(w, err)
		return
	}
	if err := validateED2KSearchParams(params); err != nil {
		writeError(w, err)
		return
	}
	status, err := s.search.Start(r.Context(), params)
	s.ed2kStatus(w, status, err)
}

func (s *Server) getED2KSearch(w http.ResponseWriter, r *http.Request) {
	if s.search == nil {
		writeJSON(w, 200, ED2KSearchStatus{Available: false, Snapshot: ED2KSearchSnapshot{
			State: ED2KSearchIdle, Params: ED2KSearchParams{Scope: ED2KScopeAll},
			Results: []ED2KSearchResult{},
		}})
		return
	}
	status, err := s.search.Snapshot(r.Context())
	s.ed2kStatus(w, status, err)
}

func (s *Server) stopED2KSearch(w http.ResponseWriter, r *http.Request) {
	if s.search == nil {
		writeError(w, core.Err(core.E_CAPABILITY_UNAVAILABLE, "eD2k search is disabled"))
		return
	}
	status, err := s.search.Stop(r.Context())
	s.ed2kStatus(w, status, err)
}

func validateED2KSearchParams(p ED2KSearchParams) error {
	q := strings.TrimSpace(p.Query)
	if q == "" || len(q) > 200 {
		return core.Err(core.E_INVALID_INPUT, "query must be 1..200 characters")
	}
	switch p.Scope {
	case "", ED2KScopeAll, ED2KScopeServer, ED2KScopeKAD:
	default:
		return core.Err(core.E_INVALID_INPUT, "invalid search scope")
	}
	if p.MinSize < 0 || p.MaxSize < 0 || (p.MaxSize > 0 && p.MinSize > p.MaxSize) {
		return core.Err(core.E_INVALID_INPUT, "invalid size range")
	}
	if p.MinSources < 0 || p.MinCompleteSources < 0 {
		return core.Err(core.E_INVALID_INPUT, "source counts must not be negative")
	}
	if len(p.FileType) > 64 || len(p.Extension) > 64 {
		return core.Err(core.E_INVALID_INPUT, "file type / extension is too long")
	}
	return nil
}
