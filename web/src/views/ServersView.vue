<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import Icon from '@/components/Icon.vue'
import EmptyState from '@/components/EmptyState.vue'
import { useED2KNetworkStore } from '@/stores/ed2kNetwork'
import { useUiStore } from '@/stores/ui'

const network = useED2KNetworkStore()
const ui = useUiStore()
const router = useRouter()
const showAdd = ref(false)
const address = ref('')
const name = ref('')
const description = ref('')
const updating = ref(false)
let timer: ReturnType<typeof setInterval> | null = null

const rows = computed(() => network.servers.servers)
const connectedCount = computed(() => rows.value.filter((row) => row.connected).length)

async function refresh(): Promise<void> {
  try {
    await network.refreshServers()
  } catch (cause) {
    ui.error(cause)
  }
}

async function add(): Promise<void> {
  if (!address.value.trim()) {
    ui.error(new Error('请填写服务器地址，格式为 host:port'))
    return
  }
  updating.value = true
  try {
    await network.addServer(address.value.trim(), name.value.trim(), description.value.trim())
    ui.success('服务器已加入 server.met')
    address.value = ''
    name.value = ''
    description.value = ''
    showAdd.value = false
  } catch (cause) {
    ui.error(cause)
  } finally {
    updating.value = false
  }
}

async function remove(row: { address: string; name: string }): Promise<void> {
  if (!window.confirm(`从 server.met 中删除服务器「${row.name || row.address}」？`)) return
  updating.value = true
  try {
    await network.removeServer(row.address)
    ui.success('服务器已从 server.met 删除')
  } catch (cause) {
    ui.error(cause)
  } finally {
    updating.value = false
  }
}

async function connect(row: { address: string }): Promise<void> {
  updating.value = true
  try {
    await network.connectServer(row.address)
    ui.success(`正在连接 ${row.address}`)
  } catch (cause) {
    ui.error(cause)
  } finally {
    updating.value = false
  }
}

async function disconnect(): Promise<void> {
  updating.value = true
  try {
    await network.disconnectServers()
    ui.success('已断开所有 eD2k 服务器')
  } catch (cause) {
    ui.error(cause)
  } finally {
    updating.value = false
  }
}

// eMule's "Update server list when a client connects": the engine records the
// server point from each HelloAnswer; this button folds those addresses into
// server.met through the same AddServer path used by manual entries.
async function updateFromPeers(): Promise<void> {
  updating.value = true
  try {
    await network.updateServersFromPeers()
    ui.success(network.servers.message || '已从连接节点更新服务器列表')
  } catch (cause) {
    ui.error(cause)
  } finally {
    updating.value = false
  }
}

onMounted(() => {
  void refresh()
  timer = setInterval(() => { if (!document.hidden && !showAdd.value) void refresh() }, 6000)
})
onUnmounted(() => { if (timer) clearInterval(timer) })
</script>

<template>
  <div class="view servers-view">
    <section class="panel">
      <header class="panel__head panel__head--toolbar">
        <div>
          <h2>eD2k 服务器列表</h2>
          <p>读取 server.met，显示连接状态；可添加、删除和连接服务器</p>
        </div>
        <div class="toolbar-actions">
          <button class="btn btn--ghost" :disabled="updating || network.busy" @click="refresh">
            <Icon name="refresh" :size="15" /> 刷新
          </button>
          <button class="btn btn--ghost" :disabled="!network.servers.connected || updating || network.busy" @click="disconnect">
            <Icon name="close" :size="15" /> 断开全部
          </button>
          <button class="btn btn--ghost" :disabled="updating || network.busy || !network.servers.available"
            title="读取已连接客户端 HelloAnswer 中的服务器地址，写入 server.met" @click="updateFromPeers">
            <Icon name="link" :size="15" /> 从连接更新
          </button>
          <button class="btn btn--primary" @click="showAdd = !showAdd">
            <Icon :name="showAdd ? 'close' : 'plus'" :size="15" /> {{ showAdd ? '取消' : '添加服务器' }}
          </button>
        </div>
      </header>

      <div v-if="showAdd" class="server-add-form">
        <label class="field">
          <span class="field__label">地址</span>
          <input v-model="address" type="text" placeholder="例如 91.228.82.26:4661" />
        </label>
        <label class="field">
          <span class="field__label">名称</span>
          <input v-model="name" type="text" placeholder="留空则使用地址" />
        </label>
        <label class="field field--full">
          <span class="field__label">描述（可选）</span>
          <input v-model="description" type="text" placeholder="服务器备注" />
        </label>
        <div class="form-actions form-actions--end">
          <button class="btn btn--primary" :disabled="updating" @click="add">
            <Icon name="check" :size="15" /> 保存到 server.met
          </button>
        </div>
      </div>

      <div class="ed-stats">
        <div class="ed-stat">
          <span>引擎状态</span>
          <strong>{{ !network.servers.available ? '未启用' : network.servers.engineStarted ? '运行中' : '待启动' }}</strong>
        </div>
        <div class="ed-stat">
          <span>已连接服务器</span>
          <strong>{{ connectedCount }} / {{ rows.length }}</strong>
        </div>
        <div class="ed-stat">
          <span>当前主服务器</span>
          <strong class="ed-stat__text">{{ network.servers.connected || '—' }}</strong>
        </div>
        <div class="ed-stat">
          <span>server.met</span>
          <strong class="ed-stat__text">{{ network.servers.serverMet || '未配置' }}</strong>
        </div>
      </div>

      <div v-if="network.servers.message" class="notice notice--warn">
        <Icon name="alert" :size="15" />
        <span>{{ network.servers.message }}</span>
        <button class="link-btn" @click="router.push({ name: 'settings', query: { tab: 'emule' } })">前往设置</button>
      </div>

      <div v-if="network.serversLoading && !rows.length" class="table-loading">正在读取服务器列表…</div>
      <EmptyState
        v-else-if="!rows.length"
        icon="server"
        title="服务器列表为空"
        hint="配置 server.met 路径后即可添加服务器，或从 eMule/aMule 导入现有 server.met"
      />
      <div v-else class="task-table__wrap">
        <table class="task-table ed-table">
          <thead>
            <tr>
              <th>地址</th>
              <th>名称</th>
              <th>描述</th>
              <th>状态</th>
              <th>来源</th>
              <th class="task-table__actions-head">操作</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="row in rows" :key="row.address">
              <td class="mono">{{ row.address }}</td>
              <td>{{ row.name || '—' }}</td>
              <td class="muted-cell">{{ row.description || '—' }}</td>
              <td>
                <span v-if="row.connected" class="chip chip--success">
                  {{ row.handshakeComplete ? '已连接' : '握手中' }}
                </span>
                <span v-else class="chip chip--soft">未连接</span>
                <span v-if="row.connected" class="chip" :class="row.clientId >= 16777216 ? 'chip--success' : 'chip--warn'"
                  :title="`服务器分配的 clientId: ${row.clientId}`">
                  {{ row.clientId >= 16777216 ? 'HighID' : 'LowID' }}
                </span>
              </td>
              <td><span class="chip chip--soft">{{ row.source }}</span></td>
              <td class="task-table__actions">
                <button class="icon-btn" title="连接" :disabled="updating || network.busy" @click="connect(row)">
                  <Icon name="play" :size="14" />
                </button>
                <button v-if="row.source === 'server.met'" class="icon-btn icon-btn--danger" title="删除"
                  :disabled="updating || network.busy" @click="remove(row)">
                  <Icon name="trash" :size="14" />
                </button>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </section>
  </div>
</template>
