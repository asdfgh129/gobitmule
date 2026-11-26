// Wire types mirror internal/core (docs/07 §1-§4). Keep field names identical:
// this file is the only place where the Go contract is transcribed into TS.

export type TaskState =
  | 'Pending' | 'Resolving' | 'Ready' | 'Running' | 'Paused'
  | 'PostProcessing' | 'Completed' | 'Failed' | 'Canceled' | 'Seeding'

export type InputKind =
  | 'url' | 'magnet' | 'torrent' | 'metalink' | 'thunder'
  | 'site' | 'telegram' | 'ed2k' | 'unknown'

export type ResourceKind = 'single' | 'multi' | 'stream' | 'torrent' | 'ed2k'

export interface CoreError {
  code: string
  message: string
  retryable: boolean
  details?: Record<string, unknown>
}

export interface ResourceFile {
  index: number
  path: string
  size: number
  offset?: number
  selected: boolean
}

export interface Mirror {
  url: string
  priority?: number
  geo?: string
  weight?: number
  failures?: number
}

export interface ProtocolIdentity {
  site?: string
  videoId?: string
  variantId?: string
  format?: string
  [key: string]: unknown
}

export interface Resource {
  id: string
  kind: ResourceKind
  engine: string
  files: ResourceFile[]
  mirrors: Mirror[]
  checksum?: { algo: string; whole?: string; pieces?: string[] }
  headers?: Record<string, string>
  cred_ref?: string
  identity: ProtocolIdentity
  post_process?: unknown[]
  limits?: Record<string, unknown>
}

export interface TaskOptions {
  dir?: string
  out?: string
  maxConnections?: number
  maxDownloadLimit?: string
  priority?: number
  postProcess?: string
  chunkSize?: number
  credentialRef?: string
  headers?: Record<string, string>
  selectFiles?: number[]
}

export interface Task {
  id: string
  state: TaskState
  input: string
  inputKind: InputKind
  resourceId?: string
  priority: number
  errorKind?: string
  errorMessage?: string
  bytesDone: number
  bytesTotal: number
  bytesUploaded: number
  createdAt: number
  updatedAt: number
  completedAt?: number
  options: TaskOptions
  resource?: Resource
}

/** One live eD2k peer of a download task (eMule source list equivalent). */
export interface TaskSource {
  endpoint: string
  userHash?: string
  client?: string
  version?: string
  sources: string[]
  state: string
  queueRank?: number
  remoteQueueRank?: number
  progress: number
  downloadSpeed: number
  uploadSpeed: number
  failCount: number
  fileName?: string
  /** ms until the next connect attempt (waiting / quarantined sources). */
  retryIn?: number
  /** Failed handshakes already recorded against this endpoint. */
  deadStrikes?: number
}

export interface TaskDetail extends Task {
  speed: number
  eta: number
  connections: number
  bitfield: string
  numPieces: number
  pieceLength: number
  followedBy?: string[]
  belongsTo?: string
  peers?: string[]
  trackers?: string[]
  selectedFiles?: number[]
  chunksVerified: number
  sources?: TaskSource[]
}

export interface TaskList {
  tasks: Task[]
  total: number
  nextCursor?: string
}

export interface CreatedTask {
  id: string
  state: TaskState
  createdAt: number
  resource: Resource | null
  resolved: boolean
  task: TaskDetail
  previewTimedOut?: boolean
  idempotent?: boolean
}

export interface BatchResult {
  ids: string[]
  count: number
}

export interface Stats {
  tasks: number
  running: number
  completed: number
  failed: number
  bytesDone: number
  connections: number
  rate: number
}

export interface Health {
  status: string
  version: string
  db: boolean
  limiter: boolean
  uptimeMs: number
  sidecars: Record<string, string>
}

export interface Capabilities {
  resolvers: string[]
  transports: string[]
  postProcessors: string[]
  sidecars: string[]
  rpcLevel: string
  credentials: string
}

export type EventType =
  | 'task.created' | 'task.state' | 'task.progress' | 'task.chunk.failed'
  | 'task.completed' | 'task.failed' | 'task.canceled'
  | 'capability.changed' | 'config.changed'

export interface UDEvent {
  type: EventType
  taskId?: string
  ts: number
  payload?: Record<string, unknown>
}

export interface WsTicket {
  ticket: string
  expiresAt: number
}

// eD2k settings are UI-managed and persisted to config.yaml by the daemon.
export interface ED2KConfig {
  enabled: boolean
  obfuscation: boolean
  secureIdent: boolean
  tcpPort: number
  udpPort: number
  outputDir: string
  nodesDat: string
  serverMet: string
}

// The daemon returns the full config; the dotted keys below are patchable at
// runtime (config.Manager.Patch whitelist). Everything else is startup-only.
export interface AppConfig {
  listen: string
  downloadDir: string
  tempSuffix: string
  logLevel: string
  global: {
    maxConcurrentTasks: number
    maxDownloadRate: string
    maxUploadRate: string
    maxConnectionsPerTask: number
    chunkSize: string
    diskFlushInterval: string
  }
  security: {
    allowedSchemes: string[]
    allowedPorts: number[]
    maxRedirects: number
    blockPrivateNetworks: boolean
    [key: string]: unknown
  }
  bt: Record<string, unknown>
  ed2k: ED2KConfig
  plugins: Record<string, unknown>
  events: Record<string, unknown>
  postProcess: Record<string, unknown>
  sources: Record<string, unknown>
  telemetry: Record<string, unknown>
}

export const PATCHABLE_KEYS = [
  'global.maxDownloadRate',
  'global.maxUploadRate',
  'global.maxConcurrentTasks',
  'global.maxConnectionsPerTask',
  'global.chunkSize',
  'security.blockPrivateNetworks',
  'ed2k.enabled',
  'ed2k.obfuscation',
  'ed2k.secureIdent',
  'ed2k.tcpPort',
  'ed2k.udpPort',
  'ed2k.outputDir',
  'ed2k.nodesDat',
  'ed2k.serverMet',
] as const

export type PatchableKey = (typeof PATCHABLE_KEYS)[number]

export interface CredentialResult {
  credentialRef: string
}

// ---------- eD2k search (mirrors internal/api/search.go) ----------
export type ED2KSearchScope = 'all' | 'server' | 'kad'
export type ED2KSearchState = 'IDLE' | 'RUNNING' | 'FINISHED' | 'STOPPED' | 'FAILED'
export type ED2KSearchResultSource = 'server' | 'kad' | 'both' | ''

export interface ED2KSearchParams {
  query: string
  scope: ED2KSearchScope
  minSize: number
  maxSize: number
  minSources: number
  minCompleteSources: number
  fileType: string
  extension: string
  fileTypes: string[]
  sources: string[]
  extensions: string[]
}

export interface ED2KSearchResult {
  hash: string
  fileName: string
  fileSize: number
  sources: number
  completeSources: number
  mediaBitrate: number
  mediaLength: number
  mediaCodec: string
  extension: string
  fileType: string
  source: ED2KSearchResultSource
  ed2kLink: string
}

export interface ED2KSearchSnapshot {
  id: number
  params: ED2KSearchParams
  state: ED2KSearchState
  results: ED2KSearchResult[]
  startedAt: number
  updatedAt: number
  serverBusy: boolean
  dhtBusy: boolean
  kadKeyword: string
  error: string
}

export interface ED2KSearchStatus {
  available: boolean
  snapshot: ED2KSearchSnapshot
}

export const EMPTY_ED2K_SEARCH_STATUS: ED2KSearchStatus = {
  available: false,
  snapshot: {
    id: 0,
    params: {
      query: '', scope: 'all', minSize: 0, maxSize: 0,
      minSources: 0, minCompleteSources: 0, fileType: '', extension: '',
      fileTypes: [], sources: [], extensions: [],
    },
    state: 'IDLE',
    results: [],
    startedAt: 0,
    updatedAt: 0,
    serverBusy: false,
    dhtBusy: false,
    kadKeyword: '',
    error: '',
  },
}

// ---------- eD2k server list / KAD nodes (mirrors internal/api/ed2k_network.go) ----------
export interface ED2KServerEntry {
  address: string
  name: string
  description: string
  source: string
  connected: boolean
  handshakeComplete: boolean
  clientId: number
}

export interface ED2KServersStatus {
  available: boolean
  engineStarted: boolean
  serverMet: string
  connected: string
  servers: ED2KServerEntry[]
  message: string
}

export interface ED2KKadNode {
  id: string
  address: string
  tcpPort: number
  version: number
  seed: boolean
  helloSent: boolean
  pinged: boolean
  responded: boolean
  failCount: number
  firstSeen: number
  lastSeen: number
  router: boolean
}

export interface ED2KKadStatus {
  available: boolean
  engineStarted: boolean
  bootstrapped: boolean
  firewalled: boolean
  liveNodes: number
  replacementNodes: number
  routerNodes: number
  runningTraversals: number
  knownNodes: number
  verifiedNodes: number
  listenPort: number
  storagePoint: string
  selfId: string
  nodes: ED2KKadNode[]
  message: string
}
