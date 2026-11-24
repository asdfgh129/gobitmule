<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import Icon from '@/components/Icon.vue'
import { getToken, setToken } from '@/api/client'
import { useSessionStore } from '@/stores/session'
import { useUiStore } from '@/stores/ui'
import type { ED2KConfig, PatchableKey } from '@/types/api'

const session = useSessionStore()
const ui = useUiStore()
const router = useRouter()
const route = useRoute()
const activeTab = ref<'general' | 'emule'>(route.query.tab === 'emule' ? 'emule' : 'general')

const draft = reactive<Partial<Record<PatchableKey, string | number | boolean>>>({
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

const ed2k = reactive<ED2KConfig>({
  enabled: false, obfuscation: true, secureIdent: true,
  tcpPort: 4662, udpPort: 4672, outputDir: '', nodesDat: '', serverMet: '',
})
const ed2kDirty = ref(false)
const ed2kSaving = ref(false)

function syncED2K(): void {
  const cfg = session.config
  if (!cfg) return
  Object.assign(ed2k, cfg.ed2k)
  ed2kDirty.value = false
}

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

onMounted(() => { syncDraft(); syncED2K() })
watch(() => session.config, () => {
  if (!dirty.value) syncDraft()
  if (!ed2kDirty.value) syncED2K()
})

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

async function saveED2K(): Promise<void> {
  if (ed2k.tcpPort < 0 || ed2k.tcpPort > 65535 || ed2k.udpPort < 0 || ed2k.udpPort > 65535) {
    ui.error(new Error('端口必须在 0–65535 之间'))
    return
  }
  ed2kSaving.value = true
  try {
    await session.patchConfig({
      'ed2k.enabled': ed2k.enabled,
      'ed2k.obfuscation': ed2k.obfuscation,
      'ed2k.secureIdent': ed2k.secureIdent,
      'ed2k.tcpPort': ed2k.tcpPort,
      'ed2k.udpPort': ed2k.udpPort,
      'ed2k.outputDir': ed2k.outputDir,
      'ed2k.nodesDat': ed2k.nodesDat,
      'ed2k.serverMet': ed2k.serverMet,
    })
    ui.success('eMule 设置已保存到 config.yaml；下载通道相关变更需重启守护进程')
    ed2kDirty.value = false
  } catch (cause) {
    ui.error(cause)
  } finally {
    ed2kSaving.value = false
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
    <div class="tabs settings-tabs">
      <button :class="{ active: activeTab === 'general' }" @click="activeTab = 'general'">
        <Icon name="settings" :size="14" /> 常规设置
      </button>
      <button :class="{ active: activeTab === 'emule' }" @click="activeTab = 'emule'">
        <Icon name="download" :size="14" /> eMule 设置
      </button>
    </div>

    <template v-if="activeTab === 'general'">
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
    </template>

    <template v-else>
      <section class="panel emule-settings">
        <header class="panel__head panel__head--toolbar">
          <div>
            <h2>eMule / eD2k 网络</h2>
            <p>保存到 config.yaml；搜索与服务器/KAD 页面即时生效，下载通道需重启守护进程</p>
          </div>
          <span v-if="ed2kDirty" class="chip chip--warn">有未保存修改</span>
        </header>
        <div class="settings-grid emule-grid">
          <div class="field field--full">
            <span class="field__label">启用 eD2k 引擎</span>
            <label class="switch">
              <input v-model="ed2k.enabled" type="checkbox" @change="ed2kDirty = true" />
              <span class="switch__track" />
              <span>{{ ed2k.enabled ? '已开启' : '已关闭' }}</span>
            </label>
            <span class="field__hint">关闭时所有 eMule 能力不可用，且不发起任何网络连接</span>
          </div>
          <label class="field">
            <span class="field__label">TCP 监听端口</span>
            <input v-model.number="ed2k.tcpPort" type="number" min="0" max="65535" @input="ed2kDirty = true" />
            <span class="field__hint">eD2k 客户端端口，默认 4662</span>
          </label>
          <label class="field">
            <span class="field__label">UDP 监听端口</span>
            <input v-model.number="ed2k.udpPort" type="number" min="0" max="65535" @input="ed2kDirty = true" />
            <span class="field__hint">KAD 与服务器 UDP 端口，默认 4672</span>
          </label>
          <label class="field field--full">
            <span class="field__label">KAD 引导文件（nodes.dat）</span>
            <input v-model="ed2k.nodesDat" type="text" placeholder="例如 /path/to/nodes.dat" @input="ed2kDirty = true" />
            <span class="field__hint">可用逗号分隔多个路径；留空则 KAD 节点页无法引导</span>
          </label>
          <label class="field field--full">
            <span class="field__label">服务器列表（server.met）</span>
            <input v-model="ed2k.serverMet" type="text" placeholder="例如 /path/to/server.met" @input="ed2kDirty = true" />
            <span class="field__hint">本地文件路径；服务器列表页可添加/删除条目</span>
          </label>
          <label class="field field--full">
            <span class="field__label">eD2k 完成目录（outputDir）</span>
            <input v-model="ed2k.outputDir" type="text" placeholder="例如 /path/to/downloads/ed2k" @input="ed2kDirty = true" />
            <span class="field__hint">仅 eD2k 下载任务使用；搜索与节点浏览无需配置</span>
          </label>
          <div class="field">
            <span class="field__label">安全标识（Secure Ident）</span>
            <label class="switch">
              <input v-model="ed2k.secureIdent" type="checkbox" @change="ed2kDirty = true" />
              <span class="switch__track" />
              <span>{{ ed2k.secureIdent ? '已开启' : '已关闭' }}</span>
            </label>
          </div>
          <div class="field">
            <span class="field__label">基本混淆（Obfuscation）</span>
            <label class="switch">
              <input v-model="ed2k.obfuscation" type="checkbox" @change="ed2kDirty = true" />
              <span class="switch__track" />
              <span>{{ ed2k.obfuscation ? '已开启' : '已关闭' }}</span>
            </label>
          </div>
        </div>
        <div class="form-actions form-actions--end">
          <button class="btn btn--ghost" :disabled="!ed2kDirty" @click="syncED2K">放弃修改</button>
          <button class="btn btn--primary" :disabled="!ed2kDirty || ed2kSaving" @click="saveED2K">
            <Icon name="check" :size="15" /> {{ ed2kSaving ? '保存中…' : '保存 eMule 设置' }}
          </button>
        </div>
      </section>

      <section class="panel">
        <header class="panel__head">
          <div><h2>网络管理</h2><p>查看服务器与 KAD 节点的实时状态</p></div>
        </header>
        <div class="emule-shortcuts">
          <button class="emule-shortcut" @click="router.push({ name: 'servers' })">
            <Icon name="server" :size="20" />
            <span><strong>服务器列表</strong><small>管理 server.met、连接 eD2k 服务器</small></span>
            <Icon name="chevron" :size="16" />
          </button>
          <button class="emule-shortcut" @click="router.push({ name: 'kad' })">
            <Icon name="wifi" :size="20" />
            <span><strong>KAD 节点</strong><small>查看路由表、添加引导节点</small></span>
            <Icon name="chevron" :size="16" />
          </button>
        </div>
      </section>
    </template>
  </div>
</template>
