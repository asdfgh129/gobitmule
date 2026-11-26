import type { EventType } from '@/types/api'

export function formatBytes(value: number): string {
  if (!Number.isFinite(value) || value <= 0) return '0 B'
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB']
  const i = Math.min(units.length - 1, Math.floor(Math.log(value) / Math.log(1024)))
  return `${(value / 1024 ** i).toFixed(i === 0 ? 0 : 1)} ${units[i]}`
}

export function formatRate(bytesPerSecond: number): string {
  return bytesPerSecond > 0 ? `${formatBytes(bytesPerSecond)}/s` : '—'
}

export function formatEta(ms: number): string {
  if (!Number.isFinite(ms) || ms <= 0) return '—'
  const seconds = Math.round(ms / 1000)
  if (seconds < 60) return `${seconds}s`
  const minutes = Math.floor(seconds / 60)
  if (minutes < 60) return `${minutes}m ${seconds % 60}s`
  return `${Math.floor(minutes / 60)}h ${minutes % 60}m`
}

export function formatTime(ts: number): string {
  if (!ts) return '—'
  return new Date(ts).toLocaleString('zh-CN', { hour12: false })
}

export function relativeTime(ts: number): string {
  if (!ts) return '—'
  const seconds = Math.max(0, Math.floor((Date.now() - ts) / 1000))
  if (seconds < 5) return '刚刚'
  if (seconds < 60) return `${seconds} 秒前`
  if (seconds < 3600) return `${Math.floor(seconds / 60)} 分钟前`
  if (seconds < 86400) return `${Math.floor(seconds / 3600)} 小时前`
  return formatTime(ts)
}

export function shortId(id: string): string {
  return id.length > 12 ? `${id.slice(0, 6)}…${id.slice(-4)}` : id
}

function baseName(path: string): string {
  const trimmed = path.replace(/[\\/]+$/, '')
  const cut = Math.max(trimmed.lastIndexOf('/'), trimmed.lastIndexOf('\\'))
  return cut >= 0 ? trimmed.slice(cut + 1) : trimmed
}

function safeDecode(value: string): string {
  try {
    return decodeURIComponent(value)
  } catch {
    return value
  }
}

/**
 * Tasks are identified by their raw link, which is unreadable in a list. eMule
 * shows the file name only and keeps the link in the property dialog, so the
 * same rule applies here: the name comes from the resolved file, then from the
 * link structure (ed2k/magnet/URL), then from the raw text as a last resort.
 */
export function taskDisplayName(task: {
  input: string
  inputKind?: string
  resource?: { files?: Array<{ path: string }> }
}): string {
  const resolved = task.resource?.files?.[0]?.path
  if (resolved) {
    const name = baseName(resolved)
    if (name) return name
  }
  const input = (task.input ?? '').trim()
  if (!input) return '未命名任务'

  if (/^ed2k:\/\//i.test(input)) {
    // ed2k://|file|<name>|<size>|<hash>|/  (or |file|...|/p=...|/)
    // Splitting the whole link keeps "ed2k://" as field 0, so the type tag and
    // the name sit at 1 and 2. Slicing the scheme off first shifted both by one
    // and silently fell through to the raw-link fallback, which is how task rows
    // ended up showing "|file|Vixen - ..." instead of the file name.
    const fields = input.split('|')
    if (fields[1]?.toLowerCase() === 'file' && fields[2]) return safeDecode(fields[2])
  }
  if (/^magnet:/i.test(input)) {
    const match = /[?&]dn=([^&]+)/i.exec(input)
    if (match?.[1]) return safeDecode(match[1]).replace(/\+/g, ' ')
  }
  if (/^https?:\/\//i.test(input)) {
    try {
      const name = baseName(new URL(input).pathname)
      if (name) return safeDecode(name)
    } catch {
      /* fall through to the raw text */
    }
  }
  const plain = baseName(input)
  const name = plain && plain.length < input.length ? plain : input
  return name.length > 90 ? `${name.slice(0, 87)}…` : name
}

/** Source states follow the eMule client list wording. */
const sourceStateLabels: Record<string, string> = {
  downloading: '下载中',
  requesting: '请求中',
  connected: '已连接',
  queued: '排队中',
  handshaking: '握手中',
  connecting: '连接中',
  waiting: '等待中',
  unconnectable: '低 ID 源',
  disconnecting: '断开中',
  dead: '已失效',
}

export function sourceStateLabel(state: string): string {
  return sourceStateLabels[state] || state || '未知'
}

// Mirrors goed2k.sourceStateRank so the console can order the source list the
// way an eMule client list reads: sources moving bytes first, then queue and
// handshake, then idle candidates, then quarantined ones.
const sourceStateRanks: Record<string, number> = {
  downloading: 0,
  requesting: 1,
  connected: 2,
  queued: 3,
  handshaking: 4,
  connecting: 5,
  waiting: 6,
  unconnectable: 7,
  disconnecting: 8,
  dead: 9,
}

export function sourceStateRank(state: string): number {
  const rank = sourceStateRanks[state]
  return rank === undefined ? 10 : rank
}

/** Short countdown used next to a waiting/quarantined source. */
export function formatCountdown(ms: number): string {
  if (!Number.isFinite(ms) || ms <= 0) return ''
  const seconds = Math.ceil(ms / 1000)
  if (seconds < 60) return `${seconds}s`
  const minutes = Math.floor(seconds / 60)
  if (minutes < 60) return `${minutes}m`
  return `${Math.floor(minutes / 60)}h`
}

export function eventLabel(type: EventType): string {
  const labels: Record<EventType, string> = {
    'task.created': '任务创建',
    'task.state': '状态变化',
    'task.progress': '传输进度',
    'task.chunk.failed': '分片重试',
    'task.completed': '下载完成',
    'task.failed': '任务失败',
    'task.canceled': '任务取消',
    'capability.changed': '能力变更',
    'config.changed': '配置更新',
  }
  return labels[type] ?? type
}
