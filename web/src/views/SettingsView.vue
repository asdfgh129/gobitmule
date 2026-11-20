<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'
import Icon from '@/components/Icon.vue'
import { getToken, setToken } from '@/api/client'
import { useSessionStore } from '@/stores/session'
import { useUiStore } from '@/stores/ui'
import type { PatchableKey } from '@/types/api'

const session = useSessionStore()
const ui = useUiStore()

const draft = reactive<Record<PatchableKey, string | number | boolean>>({
  'global.maxDownloadRate': '',
  'global.maxUploadRate': '',
  'global.maxConcurrentTasks': 5,
  'global.maxConnectionsPerTask': 16,
  'global.chunkSize': '',
  'security.blockPrivateNetworks': true,
})
const dirty = ref(false)
const saving = ref(false)
const tokenInput = ref(getToken())

function syncDraft(): void {
  const cfg = session.config
  if (!cfg) return
  draft['global.maxDownloadRate'] = cfg.global.maxDownloadRate
  draft['global.maxUploadRate'] = cfg.global.maxUploadRate
  draft['global.maxConcurrentTasks'] = cfg.global.maxConcurrentTasks
  draft['global.maxConnectionsPerTask'] = cfg.global.maxConnectionsPerTask
  draft['global.chunkSize'] = cfg.global.chunkSize
  draft['security.blockPrivateNetworks'] = cfg.security.blockPrivateNetworks
  dirty.value = false
}

onMounted(syncDraft)
watch(() => session.config, () => { if (!dirty.value) syncDraft() })

const fields: Array<{ key: PatchableKey; label: string; hint: string; type: 'text' | 'number' | 'bool' }> = [
  { key: 'global.maxDownloadRate', label: '全局下载限速', hint: '例如 8MiB/s，留空表示不限速', type: 'text' },
  { key: 'global.maxUploadRate', label: '全局上传限速', hint: '做种上传也受此约束', type: 'text' },
  { key: 'global.maxConcurrentTasks', label: '最大并发任务数', hint: '同时处于传输状态的任务上限', type: 'number' },
  { key: 'global.maxConnectionsPerTask', label: '单任务最大连接数', hint: '分片下载的并发连接上限', type: 'number' },
  { key: 'global.chunkSize', label: '分片大小', hint: '例如 4MiB', type: 'text' },
  { key: 'security.blockPrivateNetworks', label: '阻断私网访问', hint: 'SSRF 防护，默认开启', type: 'bool' },
]

const readonlyGroups = computed(() => {
  const cfg = session.config
  if (!cfg) return []
  return [
    { title: 'BT / 做种策略', icon: 'layers', value: cfg.bt },
    { title: '站点与 Sidecar 来源', icon: 'link', value: cfg.sources },
    { title: '后处理', icon: 'bolt', value: cfg.postProcess },
    { title: '插件与事件', icon: 'shield', value: { plugins: cfg.plugins, events: cfg.events } },
    { title: '网络与安全', icon: 'server', value: { listen: cfg.listen, tempSuffix: cfg.tempSuffix, logLevel: cfg.logLevel, security: cfg.security, telemetry: cfg.telemetry } },
  ]
})

function markDirty(): void { dirty.value = true }

async function save(): Promise<void> {
  saving.value = true
  try {
    await session.patchConfig({ ...draft })
    ui.success('运行参数已更新')
    dirty.value = false
  } catch (cause) {
    ui.error(cause)
  } finally {
    saving.value = false
  }
}

function applyToken(): void {
  setToken(tokenInput.value)
  ui.success(tokenInput.value ? '连接令牌已保存到本地浏览器' : '已清除连接令牌')
  void session.refresh()
}
</script>

<template>
  <div class="view settings-view">
    <section class="panel">
      <header class="panel__head">
        <div>
          <h2>运行参数</h2>
          <p>以下 6 项可通过 REST 热更新，其余配置需重启守护进程后生效</p>
        </div>
        <span v-if="dirty" class="chip chip--warn">有未保存修改</span>
      </header>
      <div class="settings-grid">
        <label v-for="field in fields" :key="field.key" class="field">
          <span class="field__label">{{ field.label }}</span>
          <input
            v-if="field.type === 'text'"
            v-model="draft[field.key]"
            type="text"
            @input="markDirty"
          />
          <input
            v-else-if="field.type === 'number'"
            v-model.number="draft[field.key]"
            type="number"
            min="0"
            @input="markDirty"
          />
          <label v-else class="switch">
            <input v-model="draft[field.key]" type="checkbox" @change="markDirty" />
            <span class="switch__track" />
            <span>{{ draft[field.key] ? '已开启' : '已关闭' }}</span>
          </label>
          <span class="field__hint">{{ field.hint }}</span>
        </label>
      </div>
      <div class="form-actions form-actions--end">
        <button class="btn btn--ghost" :disabled="!dirty" @click="syncDraft">放弃修改</button>
        <button class="btn btn--primary" :disabled="!dirty || saving" @click="save">
          <Icon name="check" :size="15" /> {{ saving ? '保存中…' : '保存并应用' }}
        </button>
      </div>
    </section>

    <div class="settings-columns">
      <section class="panel">
        <header class="panel__head">
          <div><h2>连接与凭据</h2><p>远程访问需要 Bearer 令牌</p></div>
        </header>
        <div class="form-body">
          <label class="field">
            <span class="field__label">Bearer 令牌</span>
            <input v-model="tokenInput" type="password" placeholder="留空表示本机回环连接" />
            <span class="field__hint">仅保存在当前浏览器；WebSocket 会自动换取一次性票据。</span>
          </label>
          <div class="form-actions form-actions--end">
            <button class="btn btn--ghost" @click="applyToken">应用令牌</button>
          </div>
          <div class="kv-grid kv-grid--tight">
            <div class="kv"><span>凭据后端</span><strong>{{ session.capabilities?.credentials || '—' }}</strong></div>
            <div class="kv"><span>RPC 级别</span><strong>{{ session.capabilities?.rpcLevel || '—' }}</strong></div>
          </div>
        </div>
      </section>

      <section class="panel">
        <header class="panel__head">
          <div><h2>能力清单</h2><p>由守护进程注册结果实时上报</p></div>
        </header>
        <div class="cap-grid">
          <div v-for="group in [
            { label: '解析器', items: session.capabilities?.resolvers ?? [] },
            { label: '传输器', items: session.capabilities?.transports ?? [] },
            { label: '后处理', items: session.capabilities?.postProcessors ?? [] },
            { label: 'Sidecar', items: session.capabilities?.sidecars ?? [] },
          ]" :key="group.label" class="cap-group">
            <div class="cap-group__title">{{ group.label }}</div>
            <div class="cap-group__chips">
              <span v-for="item in group.items" :key="item" class="chip chip--soft">{{ item }}</span>
            </div>
          </div>
        </div>
      </section>
    </div>

    <section class="panel">
      <header class="panel__head">
        <div><h2>只读配置</h2><p>启动时由配置文件 / 环境变量决定，界面不提供写入</p></div>
      </header>
      <div class="readonly-grid">
        <details v-for="group in readonlyGroups" :key="group.title" class="readonly-card">
          <summary><Icon :name="group.icon" :size="15" /> {{ group.title }}</summary>
          <pre class="code-block">{{ JSON.stringify(group.value, null, 2) }}</pre>
        </details>
      </div>
    </section>
  </div>
</template>
