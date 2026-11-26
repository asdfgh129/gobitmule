// Package core defines the protocol-agnostic model of GoBitMule.
//
// It is the ONLY package allowed to define Task / Resource / state machine /
// events, and it must never import a concrete transport or resolve package
// (docs/05 §1, docs/15 §2). Everything else depends on core, never the reverse.
package core

// ResourceKind is the shape of the output produced by a Resource.
type ResourceKind string

const (
	KindSingle  ResourceKind = "single"
	KindMulti   ResourceKind = "multi"
	KindStream  ResourceKind = "stream"
	KindTorrent ResourceKind = "torrent"
	KindED2K    ResourceKind = "ed2k"
)

// Engine selects the execution backend for one task. `byo_adapter` is reserved
// for a future task-level adapter and is NEVER a protocol-level mix (ADR-0013).
type Engine string

const (
	EngineGo         Engine = "go"
	EngineBYOAdapter Engine = "byo_adapter"
)

// HeaderStreamKind is an INTERNAL routing header the hls / dash Resolvers
// stamp onto stream Resources so the Transport registry can pick the right
// byte mover even for extension-less (force-classified) URLs. It persists in
// resources.headers (it is not sensitive) so dispatch survives restarts, and
// it NEVER reaches the wire: both transports strip it before any request.
const HeaderStreamKind = "X-Ud-Stream"

// Values of HeaderStreamKind (ADR-0024).
const (
	StreamKindHLS  = "hls"
	StreamKindDASH = "dash"
)

// InputKind classifies user input. It is not a transport protocol:
// "thunder" is a link wrapper, "site" is a content source (docs/02 术语表).
type InputKind string

const (
	InputURL      InputKind = "url"
	InputMagnet   InputKind = "magnet"
	InputTorrent  InputKind = "torrent"
	InputMetalink InputKind = "metalink"
	InputThunder  InputKind = "thunder"
	InputSite     InputKind = "site"
	InputTelegram InputKind = "telegram"
	InputED2K     InputKind = "ed2k"
	InputUnknown  InputKind = "unknown"
)

// TaskID is a ULID (docs/06 §3).
type TaskID string

// CredentialRef is an opaque reference into the credential store. Plaintext
// secrets must never appear in the DB, events, logs or REST responses.
type CredentialRef string

func (r CredentialRef) String() string { return string(r) }

// Resource is the P0-frozen unified description of "what to download".
// Field set is fixed by ADR-0013: adding BT/eD2k/stream support later must NOT
// change this struct, the DDL in docs/06 §3, or the API schema in docs/07.
type Resource struct {
	ID          string            `json:"id"`
	Kind        ResourceKind      `json:"kind"`
	Engine      Engine            `json:"engine"`
	Files       []ResourceFile    `json:"files"`
	Mirrors     []Mirror          `json:"mirrors"`
	Checksum    *Checksum         `json:"checksum,omitempty"`
	Headers     map[string]string `json:"headers,omitempty"`
	Credential  CredentialRef     `json:"cred_ref,omitempty"`
	Identity    ProtocolIdentity  `json:"identity"`
	PostProcess []ProcessSpec     `json:"post_process,omitempty"`
	Limits      TaskLimits        `json:"limits"`
}

// ResourceFile is one output file. Native multi-file support is mandatory in P0
// (BT seeds, Metalink, separated A/V streams, aria2 getFiles mapping).
type ResourceFile struct {
	Index    int    `json:"index"`
	Path     string `json:"path"` // relative path, validated by security.PathGuard
	Size     int64  `json:"size"`
	Offset   int64  `json:"offset,omitempty"`
	Selected bool   `json:"selected"`
	// URL is the per-file source for multi-file (separated-stream) tasks
	// (docs/06 §4.4, ADR-0027). Empty for single-file resources, whose URL
	// lives on Mirrors. Every non-empty URL is SSRF-guard-checked by the
	// resolver before it is persisted.
	URL string `json:"url,omitempty"`
}

// Mirror is one source URL with its scheduling metadata.
type Mirror struct {
	URL      string  `json:"url"`
	Priority int     `json:"priority,omitempty"`
	Geo      string  `json:"geo,omitempty"`
	Weight   float64 `json:"weight,omitempty"`
	Failures int     `json:"failures,omitempty"`
}

// Checksum holds whole-file and per-piece digests (Metalink / BT piece hashes).
type Checksum struct {
	Algo   string   `json:"algo"`
	Whole  string   `json:"whole,omitempty"`
	Pieces []string `json:"pieces,omitempty"`
}

// ProtocolIdentity carries every protocol-specific identity field decided in
// ADR-0013. All sensitive values are references, never plaintext.
type ProtocolIdentity struct {
	// BitTorrent
	InfoHashV1  string     `json:"infohash_v1,omitempty"`
	InfoHashV2  string     `json:"btmh,omitempty"`
	Private     bool       `json:"private,omitempty"`
	Trackers    [][]string `json:"trackers,omitempty"`
	PasskeyRef  string     `json:"passkey_ref,omitempty"`
	PeerID      string     `json:"peer_id,omitempty"`
	TorrentMeta []byte     `json:"-"` // raw metainfo, never serialized

	// eD2k
	ED2KFileHash string `json:"ed2k_file_hash,omitempty"`
	AICHRoot     string `json:"aich_root,omitempty"`
	UserHash     string `json:"user_hash,omitempty"`
	IdentKeyRef  string `json:"ident_key_ref,omitempty"`
	CreditsRef   string `json:"credits_ref,omitempty"`

	// Adaptive streams
	VariantID  string `json:"variant_id,omitempty"`
	IsAdaptive bool   `json:"is_adaptive,omitempty"`
}

// ProcessSpec names a PostProcessor step. P0 ships only "none" (passthrough).
type ProcessSpec struct {
	Type   string `json:"type"`
	Target string `json:"target,omitempty"`
}

// TaskLimits are per-task budget hints injected by the single Scheduler.
type TaskLimits struct {
	MaxConnections   int    `json:"max_connections,omitempty"`
	MaxDownloadLimit string `json:"max_download_limit,omitempty"`
	MaxUploadLimit   string `json:"max_upload_limit,omitempty"`
	ChunkSize        int64  `json:"chunk_size,omitempty"`
}

// ChunkState is persisted in `chunks.state` (docs/06 §3).
type ChunkState string

const (
	ChunkPending  ChunkState = "pending"
	ChunkAssigned ChunkState = "assigned"
	ChunkDone     ChunkState = "done"
	ChunkVerified ChunkState = "verified"
	ChunkFailed   ChunkState = "failed"
)

// Chunk is the unified unit of progress: HTTP range and BT piece share one table.
type Chunk struct {
	ID          int64      `json:"id"`
	TaskID      TaskID     `json:"taskId"`
	FileIndex   int        `json:"fileIndex"`
	Index       int        `json:"idx"`
	Start       int64      `json:"start"`
	Length      int64      `json:"length"`
	State       ChunkState `json:"state"`
	Attempt     int        `json:"attempt"`
	MirrorIndex int        `json:"mirrorIndex"`
	HashOK      bool       `json:"hashOk"`
}

// End is the exclusive upper bound of the chunk.
func (c Chunk) End() int64 { return c.Start + c.Length - 1 }

// Hash is the digest of one chunk's byte range.
type Hash struct {
	Algo string `json:"algo"`
	Sum  string `json:"sum"`
}

// ProbeResult is returned by Transport.Probe before chunk allocation.
type ProbeResult struct {
	Size         int64  `json:"size"`
	AcceptRanges bool   `json:"acceptRanges"`
	ETag         string `json:"etag,omitempty"`
	LastModified string `json:"lastModified,omitempty"`
	Filename     string `json:"filename,omitempty"`
}

// LocalFiles is the ONLY legal PostProcessor input (ADR-0012).
type LocalFiles struct {
	BaseDir string   `json:"baseDir"`
	Paths   []string `json:"paths"`
}

// OutputSpec describes the postprocessor output. Plugins write `<target>.part`;
// the core performs the atomic rename, never the plugin.
type OutputSpec struct {
	Target     string `json:"target"`
	TempSuffix string `json:"tempSuffix"`
}

// ProcessResult lists files produced by a PostProcessor (always .part paths).
type ProcessResult struct {
	Outputs   []string `json:"outputs"`
	TempPaths []string `json:"tempPaths,omitempty"`
}

// TaskOptions mirror the create-task request in docs/07 §2.
type TaskOptions struct {
	Dir              string            `json:"dir,omitempty"`
	Out              string            `json:"out,omitempty"`
	MaxConnections   int               `json:"maxConnections,omitempty"`
	MaxDownloadLimit string            `json:"maxDownloadLimit,omitempty"`
	Priority         int               `json:"priority,omitempty"`
	PostProcess      string            `json:"postProcess,omitempty"`
	ChunkSize        int64             `json:"chunkSize,omitempty"`
	CredentialRef    CredentialRef     `json:"credentialRef,omitempty"`
	Headers          map[string]string `json:"headers,omitempty"`
	SelectFiles      []int             `json:"selectFiles,omitempty"`
}

// TaskFilter is the list/pagination contract of GET /tasks.
type TaskFilter struct {
	State  TaskState `json:"state,omitempty"`
	Kind   InputKind `json:"kind,omitempty"`
	Cursor string    `json:"cursor,omitempty"`
	Limit  int       `json:"limit"`
}

// Stats is the payload of GET /stats.
type Stats struct {
	Tasks       int   `json:"tasks"`
	Running     int   `json:"running"`
	Completed   int   `json:"completed"`
	Failed      int   `json:"failed"`
	BytesDone   int64 `json:"bytesDone"`
	Connections int   `json:"connections"`
	Rate        int64 `json:"rate"`
}

// Health is the payload of GET /healthz.
type Health struct {
	Status   string            `json:"status"`
	Version  string            `json:"version"`
	DB       bool              `json:"db"`
	Limiter  bool              `json:"limiter"`
	UptimeMs int64             `json:"uptimeMs"`
	Sidecars map[string]string `json:"sidecars"`
}

// Capabilities is the payload of GET /capabilities.
type Capabilities struct {
	Resolvers      []string `json:"resolvers"`
	Transports     []string `json:"transports"`
	PostProcessors []string `json:"postProcessors"`
	Sidecars       []string `json:"sidecars"`
	RPCLevel       string   `json:"rpcLevel"`
	// Credentials names the active credential backend (ADR-0015 ④). "file"
	// means permission-protected files, not an OS keychain.
	Credentials string `json:"credentials"`
}

// TaskSource is one eD2k peer attached to a download task. It is optional and
// only populated for eD2k tasks when the engine is reachable.
type TaskSource struct {
	Endpoint        string   `json:"endpoint"`
	UserHash        string   `json:"userHash,omitempty"`
	Client          string   `json:"client,omitempty"`
	Version         string   `json:"version,omitempty"`
	Sources         []string `json:"sources"`
	State           string   `json:"state"`
	QueueRank       int      `json:"queueRank,omitempty"`
	RemoteQueueRank int      `json:"remoteQueueRank,omitempty"`
	Progress        float64  `json:"progress"`
	DownloadSpeed   int64    `json:"downloadSpeed"`
	UploadSpeed     int64    `json:"uploadSpeed"`
	FailCount       int      `json:"failCount"`
	FileName        string   `json:"fileName,omitempty"`
	// RetryIn (ms) explains a source that is waiting or quarantined instead of
	// silently disappearing from the list; DeadStrikes is how many handshakes
	// already failed against it.
	RetryIn     int64 `json:"retryIn,omitempty"`
	DeadStrikes int   `json:"deadStrikes,omitempty"`
	// ProgressKnown tells the console whether Progress is a reported value. A
	// peer that never answered FileStatusRequest must not be drawn as 0%.
	ProgressKnown bool `json:"progressKnown,omitempty"`
	// PieceMap is the peer's part bitmap (hex, MSB first, empty when the peer
	// holds the complete file) and Pieces is how many parts it covers. eMule's
	// client list draws this bitmap instead of a single percentage, which shows
	// which parts a source actually has.
	PieceMap string `json:"pieceMap,omitempty"`
	Pieces   int    `json:"pieces,omitempty"`
}

// TaskDetail is the GET /tasks/{id} payload. Fields reserved for the optional
// L1 aria2 facade (ADR-0019) are present but empty in P0.
type TaskDetail struct {
	Task
	Speed          int64    `json:"speed"`
	ETA            int64    `json:"eta"`
	Connections    int      `json:"connections"`
	Bitfield       string   `json:"bitfield"`
	NumPieces      int      `json:"numPieces"`
	PieceLength    int64    `json:"pieceLength"`
	FollowedBy     []string `json:"followedBy"`
	BelongsTo      string   `json:"belongsTo"`
	Peers          []string `json:"peers"`
	Trackers       []string `json:"trackers"`
	SelectedFiles  []int    `json:"selectedFiles"`
	ChunksVerified int      `json:"chunksVerified"`
	// Sources is populated by the API layer for eD2k tasks from the live
	// engine; it stays nil for other engines and when no engine is running.
	Sources []TaskSource `json:"sources,omitempty"`
}

// Scope identifies who consumes a Limiter budget.
type Scope struct {
	TaskID    string
	Host      string
	Transport string
	Direction Direction
}

// Direction separates download and upload accounting (docs/05 §6).
type Direction string

const (
	DirectionDownload Direction = "download"
	DirectionUpload   Direction = "upload"
)

// BytesPerSec is the unit of every rate in the system.
type BytesPerSec int64
