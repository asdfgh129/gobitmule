package goed2k

import (
	"sort"
	"strings"
	"sync"
	"unicode"

	"github.com/asdfgh129/gobitmule/internal/ed2k/protocol"
	kadproto "github.com/asdfgh129/gobitmule/internal/ed2k/protocol/kad"
	serverproto "github.com/asdfgh129/gobitmule/internal/ed2k/protocol/server"
)

type SearchScope uint8

const (
	SearchScopeServer SearchScope = 1 << iota
	SearchScopeDHT
	SearchScopeAll = SearchScopeServer | SearchScopeDHT
)

type SearchState string

const (
	SearchStateIdle     SearchState = "IDLE"
	SearchStateRunning  SearchState = "RUNNING"
	SearchStateFinished SearchState = "FINISHED"
	SearchStateStopped  SearchState = "STOPPED"
	SearchStateFailed   SearchState = "FAILED"
)

type SearchParams struct {
	Query              string
	Scope              SearchScope
	MinSize            int64
	MaxSize            int64
	MinSources         int
	MinCompleteSources int
	FileType           string
	Extension          string
	FileTypes          []string
	Sources            []string
	Extensions         []string
}

type SearchResultSource uint8

const (
	SearchResultServer SearchResultSource = 1 << iota
	SearchResultKAD
)

type SearchResult struct {
	Hash            protocol.Hash
	FileName        string
	FileSize        int64
	Sources         int
	CompleteSources int
	MediaBitrate    int
	MediaLength     int
	MediaCodec      string
	Extension       string
	FileType        string
	Source          SearchResultSource
	// Rating is the spam/quality score 0..5 from SpamFilter.Rate (#11).
	Rating int
	// Spam marks a result the filter considers fake; it is only dropped when
	// the spam filter is enabled.
	Spam bool
}

func (r SearchResult) ED2KLink() string {
	if r.FileName == "" || r.FileSize <= 0 || r.Hash == protocol.Invalid {
		return ""
	}
	return FormatLink(r.FileName, r.FileSize, r.Hash)
}

type SearchSnapshot struct {
	ID         uint32
	Params     SearchParams
	State      SearchState
	Results    []SearchResult
	UpdatedAt  int64
	StartedAt  int64
	ServerBusy bool
	DHTBusy    bool
	KadKeyword string
	Error      string
}

type SearchHandle struct {
	session *Session
	id      uint32
}

func (h SearchHandle) ID() uint32 {
	return h.id
}

func (h SearchHandle) IsValid() bool {
	return h.session != nil && h.id != 0
}

func (h SearchHandle) Snapshot() SearchSnapshot {
	if h.session == nil {
		return SearchSnapshot{}
	}
	return h.session.SearchSnapshot()
}

func (h SearchHandle) Stop() error {
	if h.session == nil {
		return nil
	}
	return h.session.StopSearch(h.id)
}

type searchTask struct {
	mu         sync.Mutex
	id         uint32
	params     SearchParams
	state      SearchState
	results    map[string]SearchResult
	startedAt  int64
	updatedAt  int64
	deadlineAt int64
	serverBusy bool
	dhtBusy    bool
	dhtPending int
	kadKeyword string
	errText    string
}

func newSearchTask(id uint32, params SearchParams, startedAt int64) *searchTask {
	return &searchTask{
		id:        id,
		params:    params,
		state:     SearchStateRunning,
		results:   make(map[string]SearchResult),
		startedAt: startedAt,
		updatedAt: startedAt,
	}
}

func (s *searchTask) snapshot() SearchSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	results := make([]SearchResult, 0, len(s.results))
	for _, result := range s.results {
		results = append(results, result)
	}
	sort.Slice(results, func(i, j int) bool {
		if results[i].Sources != results[j].Sources {
			return results[i].Sources > results[j].Sources
		}
		if results[i].CompleteSources != results[j].CompleteSources {
			return results[i].CompleteSources > results[j].CompleteSources
		}
		if results[i].FileSize != results[j].FileSize {
			return results[i].FileSize > results[j].FileSize
		}
		return results[i].FileName < results[j].FileName
	})
	return SearchSnapshot{
		ID:         s.id,
		Params:     s.params,
		State:      s.state,
		Results:    results,
		UpdatedAt:  s.updatedAt,
		StartedAt:  s.startedAt,
		ServerBusy: s.serverBusy,
		DHTBusy:    s.dhtBusy,
		KadKeyword: s.kadKeyword,
		Error:      s.errText,
	}
}

func (s *searchTask) setDeadline(deadline int64) {
	s.mu.Lock()
	if deadline > s.deadlineAt {
		s.deadlineAt = deadline
	}
	s.updatedAt = CurrentTime()
	s.mu.Unlock()
}

func (s *searchTask) finishServer() {
	s.mu.Lock()
	s.serverBusy = false
	s.finishLocked()
	s.mu.Unlock()
}

func (s *searchTask) beginDHT(keywords []string) {
	s.mu.Lock()
	s.dhtPending = len(keywords)
	s.dhtBusy = s.dhtPending > 0
	s.kadKeyword = strings.Join(keywords, " | ")
	s.updatedAt = CurrentTime()
	s.mu.Unlock()
}

func (s *searchTask) finishDHTKeyword() {
	s.mu.Lock()
	if s.dhtPending > 0 {
		s.dhtPending--
	}
	if s.dhtPending == 0 {
		s.dhtBusy = false
	}
	s.finishLocked()
	s.mu.Unlock()
}

func (s *searchTask) finishDHT() {
	s.mu.Lock()
	s.dhtPending = 0
	s.dhtBusy = false
	s.finishLocked()
	s.mu.Unlock()
}

func (s *searchTask) stop() {
	s.mu.Lock()
	s.serverBusy = false
	s.dhtBusy = false
	s.state = SearchStateStopped
	s.updatedAt = CurrentTime()
	s.mu.Unlock()
}

func (s *searchTask) fail(err error) {
	s.mu.Lock()
	s.serverBusy = false
	s.dhtBusy = false
	s.state = SearchStateFailed
	if err != nil {
		s.errText = err.Error()
	}
	s.updatedAt = CurrentTime()
	s.mu.Unlock()
}

func (s *searchTask) onTick(now int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != SearchStateRunning {
		return
	}
	if s.serverBusy && s.deadlineAt > 0 && now >= s.deadlineAt {
		s.serverBusy = false
	}
	s.finishLocked()
}

func (s *searchTask) finishLocked() {
	if s.state != SearchStateRunning {
		return
	}
	if !s.serverBusy && !s.dhtBusy {
		s.state = SearchStateFinished
	}
	s.updatedAt = CurrentTime()
}

func (s *searchTask) mergeResult(result SearchResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := result.Hash.String()
	if existing, ok := s.results[key]; ok {
		if existing.FileName == "" {
			existing.FileName = result.FileName
		}
		if existing.FileSize == 0 {
			existing.FileSize = result.FileSize
		}
		if result.Sources > existing.Sources {
			existing.Sources = result.Sources
		}
		if result.CompleteSources > existing.CompleteSources {
			existing.CompleteSources = result.CompleteSources
		}
		if existing.MediaBitrate == 0 {
			existing.MediaBitrate = result.MediaBitrate
		}
		if existing.MediaLength == 0 {
			existing.MediaLength = result.MediaLength
		}
		if existing.MediaCodec == "" {
			existing.MediaCodec = result.MediaCodec
		}
		if existing.Extension == "" {
			existing.Extension = result.Extension
		}
		if existing.FileType == "" {
			existing.FileType = result.FileType
		}
		existing.Source |= result.Source
		s.results[key] = existing
	} else {
		s.results[key] = result
	}
	s.updatedAt = CurrentTime()
}

func makeSearchResultFromServer(entry serverproto.SharedFileEntry) SearchResult {
	result := SearchResult{
		Hash:   entry.Hash,
		Source: SearchResultServer,
	}
	if name, ok := entry.StringTag(protocol.FTFilename); ok {
		result.FileName = name
	}
	if size, ok := entry.UIntTag(protocol.FTFileSize); ok {
		result.FileSize = int64(size)
	}
	if hi, ok := entry.UIntTag(protocol.FTFileSizeHi); ok {
		result.FileSize += int64(hi << 32)
	}
	if sources, ok := entry.UIntTag(protocol.FTSources); ok {
		result.Sources = int(sources)
	}
	if complete, ok := entry.UIntTag(protocol.FTCompleteSources); ok {
		result.CompleteSources = int(complete)
	}
	if bitrate, ok := entry.UIntTag(protocol.FTMediaBitrate); ok {
		result.MediaBitrate = int(bitrate)
	}
	if length, ok := entry.UIntTag(protocol.FTMediaLength); ok {
		result.MediaLength = int(length)
	}
	if codec, ok := entry.StringTag(protocol.FTMediaCodec); ok {
		result.MediaCodec = codec
	}
	if ext, ok := entry.StringTag(protocol.FTFileFormat); ok {
		result.Extension = ext
	}
	if fileType, ok := entry.StringTag(protocol.FTFileType); ok {
		result.FileType = fileType
	}
	return result
}

func makeSearchResultFromKAD(entry kadproto.SearchEntry) SearchResult {
	result := SearchResult{
		Hash:   entry.ID.Hash,
		Source: SearchResultKAD,
	}
	if name, ok := entry.StringTag(protocol.FTFilename); ok {
		result.FileName = name
	}
	if size, ok := entry.UIntTag(protocol.FTFileSize); ok {
		result.FileSize = int64(size)
	}
	if hi, ok := entry.UIntTag(protocol.FTFileSizeHi); ok {
		result.FileSize += int64(hi << 32)
	}
	if sources, ok := entry.UIntTag(protocol.FTSources); ok {
		result.Sources = int(sources)
	}
	if complete, ok := entry.UIntTag(protocol.FTCompleteSources); ok {
		result.CompleteSources = int(complete)
	}
	if bitrate, ok := entry.UIntTag(protocol.FTMediaBitrate); ok {
		result.MediaBitrate = int(bitrate)
	}
	if length, ok := entry.UIntTag(protocol.FTMediaLength); ok {
		result.MediaLength = int(length)
	}
	if codec, ok := entry.StringTag(protocol.FTMediaCodec); ok {
		result.MediaCodec = codec
	}
	if ext, ok := entry.StringTag(protocol.FTFileFormat); ok {
		result.Extension = ext
	}
	if fileType, ok := entry.StringTag(protocol.FTFileType); ok {
		result.FileType = fileType
	}
	return result
}

func normalizeSearchParams(params SearchParams) SearchParams {
	params.Query = strings.TrimSpace(params.Query)
	if params.Scope == 0 {
		params.Scope = SearchScopeAll
	}
	return params
}

// SearchQuery is an OR-list of AND groups. "windows 11 office" is one group
// requiring all three terms; "windows 11 OR windows 10" is two groups and a
// result only needs to satisfy one of them.
type SearchQuery struct {
	Groups [][]string
}

func parseSearchQuery(query string) SearchQuery {
	fields := strings.FieldsFunc(strings.TrimSpace(query), func(r rune) bool {
		return unicode.IsSpace(r) || strings.ContainsRune("|;,", r)
	})
	var groups [][]string
	current := make([]string, 0, len(fields))
	for _, field := range fields {
		token := strings.Trim(field, ` ()[]{}<>,._-!?:;\|/"'`)
		if token == "" {
			continue
		}
		switch strings.ToUpper(token) {
		case "OR":
			if len(current) > 0 {
				groups = append(groups, current)
				current = make([]string, 0, len(fields))
			}
		case "AND":
			// AND is the default; keep it as an explicit no-op for readability.
		default:
			current = append(current, token)
		}
	}
	if len(current) > 0 {
		groups = append(groups, current)
	}
	return SearchQuery{Groups: groups}
}

func (q SearchQuery) Empty() bool { return len(q.Groups) == 0 }

// Keywords returns one representative term per OR group, preferring the
// longest term. KAD can only look up one keyword hash per traversal, so each
// group gets one lookup and the full expression is enforced by local matching.
func (q SearchQuery) Keywords(limit int) []string {
	var out []string
	seen := make(map[string]bool)
	for _, group := range q.Groups {
		best := ""
		for _, term := range group {
			if seen[strings.ToLower(term)] {
				continue
			}
			if len([]byte(term)) >= 3 && len([]byte(term)) > len([]byte(best)) {
				best = term
			}
		}
		if best == "" && len(group) > 0 {
			best = group[0]
		}
		if best == "" || seen[strings.ToLower(best)] {
			continue
		}
		seen[strings.ToLower(best)] = true
		out = append(out, best)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

func (q SearchQuery) Matches(result SearchResult) bool {
	if q.Empty() {
		return true
	}
	// Keyword searches index file names, not hashes. Including the hash here
	// let short terms such as "11" match by chance inside a 32-char hash.
	fields := []string{result.FileName, result.Extension, result.FileType}
	for i := range fields {
		fields[i] = strings.ToLower(fields[i])
	}
	for _, group := range q.Groups {
		all := true
		for _, term := range group {
			needle := strings.ToLower(term)
			found := false
			for _, field := range fields {
				if strings.Contains(field, needle) {
					found = true
					break
				}
			}
			if !found {
				all = false
				break
			}
		}
		if all {
			return true
		}
	}
	return false
}

// pickKadKeyword is kept for callers that need a single representative term.
func pickKadKeyword(query string) string {
	keywords := parseSearchQuery(query).Keywords(1)
	if len(keywords) == 0 {
		return ""
	}
	return keywords[0]
}
