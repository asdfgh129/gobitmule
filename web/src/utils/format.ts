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
