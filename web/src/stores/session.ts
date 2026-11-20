import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { rpc } from '@/api/rpc'
import type { SocketState } from '@/api/eventsocket'
import type { AppConfig, Capabilities, Health, PatchableKey, Stats } from '@/types/api'

export const useSessionStore = defineStore('session', () => {
  const health = ref<Health | null>(null)
  const capabilities = ref<Capabilities | null>(null)
  const stats = ref<Stats | null>(null)
  const config = ref<AppConfig | null>(null)
  const socketState = ref<SocketState>('closed')
  const lastEventAt = ref(0)
  const lastError = ref('')
  const loading = ref(false)

  const online = computed(() => health.value?.status === 'ok')
  const uptime = computed(() => {
    const ms = health.value?.uptimeMs ?? 0
    const minutes = Math.floor(ms / 60_000)
    const hours = Math.floor(minutes / 60)
    return hours > 0 ? `${hours}h ${minutes % 60}m` : `${minutes}m`
  })

  async function refresh(): Promise<void> {
    loading.value = true
    const results = await Promise.allSettled([
      rpc.system.health(), rpc.system.capabilities(),
      rpc.system.stats(), rpc.config.get(),
    ])
    const [h, c, s, cfg] = results
    if (h.status === 'fulfilled') { health.value = h.value; lastError.value = '' }
    else lastError.value = h.reason instanceof Error ? h.reason.message : String(h.reason)
    if (c.status === 'fulfilled') capabilities.value = c.value
    if (s.status === 'fulfilled') stats.value = s.value
    if (cfg.status === 'fulfilled') config.value = cfg.value
    loading.value = false
  }

  async function patchConfig(values: Partial<Record<PatchableKey, string | number | boolean>>) {
    config.value = await rpc.config.patch(values)
  }

  return {
    health, capabilities, stats, config, socketState, lastEventAt, lastError,
    loading, online, uptime, refresh, patchConfig,
  }
})
