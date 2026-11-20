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

// The daemon returns the full config; only the six dotted keys below are
// patchable at runtime (config.Manager.Patch whitelist). Everything else is
// startup-only and is rendered read-only by the Settings view.
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
  ed2k: Record<string, unknown>
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
] as const

export type PatchableKey = (typeof PATCHABLE_KEYS)[number]

export interface CredentialResult {
  credentialRef: string
}
