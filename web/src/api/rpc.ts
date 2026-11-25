import { http } from './client'
import type {
  AppConfig, BatchResult, Capabilities, CreatedTask, CredentialResult,
  ED2KKadStatus, ED2KServersStatus, ED2KSearchParams, ED2KSearchStatus,
  Health, PatchableKey, Resource, Stats,
  Task, TaskDetail, TaskList, TaskOptions, TaskState, UDEvent, InputKind,
} from '@/types/api'

/**
 * Typed RPC surface over the daemon's L0 REST + WebSocket transport (docs/07,
 * ADR-0019). Method names are stable; the wire stays plain REST so the CLI and
 * any third-party client keep working unchanged. See docs/07 §9 for the map.
 */
export const rpc = {
  system: {
    health: () => http.get<Health>('/api/v1/healthz'),
    capabilities: () => http.get<Capabilities>('/api/v1/capabilities'),
    stats: () => http.get<Stats>('/api/v1/stats'),
  },

  tasks: {
    list(filter: { state?: TaskState | ''; kind?: InputKind | ''; cursor?: string; limit?: number } = {}) {
      const q = new URLSearchParams()
      if (filter.state) q.set('state', filter.state)
      if (filter.kind) q.set('kind', filter.kind)
      if (filter.cursor) q.set('cursor', filter.cursor)
      q.set('limit', String(filter.limit ?? 100))
      return http.get<TaskList>(`/api/v1/tasks?${q}`)
    },
    get: (id: string) => http.get<TaskDetail>(`/api/v1/tasks/${encodeURIComponent(id)}`),
    preview: (input: string, options: TaskOptions = {}) =>
      http.post<Resource>('/api/v1/resolve', { input, options }),
    create: (input: string, options: TaskOptions = {}, credentialRef = '', waitMs?: number) => {
      const query = waitMs === undefined ? '' : `?wait=${waitMs}`
      return http.post<CreatedTask>(`/api/v1/tasks${query}`, {
        input, options, credentialRef,
      })
    },
    batch: (inputs: string[], options: TaskOptions = {}, action: 'add' | 'pause' | 'resume' | 'cancel' = 'add') =>
      http.post<BatchResult>('/api/v1/tasks/batch', { inputs, action, options }),
    patch: (id: string, options: TaskOptions) =>
      http.patch<TaskDetail>(`/api/v1/tasks/${encodeURIComponent(id)}`, options),
    remove: (id: string, withFiles = false) =>
      http.delete<void>(`/api/v1/tasks/${encodeURIComponent(id)}?withFiles=${withFiles}`),
    pause: (id: string) => http.post<{ id: string; accepted: boolean }>(`/api/v1/tasks/${encodeURIComponent(id)}/pause`),
    resume: (id: string) => http.post<{ id: string; accepted: boolean }>(`/api/v1/tasks/${encodeURIComponent(id)}/resume`),
    cancel: (id: string) => http.post<{ id: string; accepted: boolean }>(`/api/v1/tasks/${encodeURIComponent(id)}/cancel`),
  },

  config: {
    get: () => http.get<AppConfig>('/api/v1/config'),
    patch: (values: Partial<Record<PatchableKey, string | number | boolean>>) =>
      http.patch<AppConfig>('/api/v1/config', values),
  },

  credentials: {
    put: (value: string) => http.post<CredentialResult>('/api/v1/credentials', { value }),
  },

  events: {
    history: (limit = 100) => http.get<{ events: UDEvent[] }>(`/api/v1/events/history?limit=${limit}`),
  },

  ed2kSearch: {
    status: () => http.get<ED2KSearchStatus>('/api/v1/ed2k/search'),
    start: (params: ED2KSearchParams) =>
      http.post<ED2KSearchStatus>('/api/v1/ed2k/search', params),
    stop: () => http.delete<ED2KSearchStatus>('/api/v1/ed2k/search'),
  },

  ed2kNetwork: {
    servers: () => http.get<ED2KServersStatus>('/api/v1/ed2k/servers'),
    addServer: (input: { address: string; name?: string; description?: string }) =>
      http.post<ED2KServersStatus>('/api/v1/ed2k/servers', input),
    removeServer: (address: string) =>
      http.delete<ED2KServersStatus>(`/api/v1/ed2k/servers?address=${encodeURIComponent(address)}`),
    connectServer: (address: string) =>
      http.post<ED2KServersStatus>('/api/v1/ed2k/servers/connect', { address }),
    disconnectServers: () => http.post<ED2KServersStatus>('/api/v1/ed2k/servers/disconnect'),
    updateServersFromPeers: () =>
      http.post<ED2KServersStatus>('/api/v1/ed2k/servers/update-from-peers'),
    kad: () => http.get<ED2KKadStatus>('/api/v1/ed2k/kad'),
    addKadNode: (address: string) =>
      http.post<ED2KKadStatus>('/api/v1/ed2k/kad/nodes', { address }),
    bootstrapKadFromPeers: () =>
      http.post<ED2KKadStatus>('/api/v1/ed2k/kad/bootstrap-from-peers'),
  },
}

/** Task rows are immutable snapshots on the wire; cloning is deliberate. */
export type { Task, TaskDetail, UDEvent }
