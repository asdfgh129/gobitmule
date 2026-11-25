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
const updating = ref(false)
let timer: ReturnType<typeof setInterval> | null = null

const nodes = computed(() => network.kad.nodes)

async function refresh(): Promise<void> {
  try {
    await network.refreshKad()
  } catch (cause) {
    ui.error(cause)
  }
}

async function add(): Promise<void> {
  if (!address.value.trim()) {
    ui.error(new Error('请填写节点地址，格式为 host:port'))
    return
  }
  updating.value = true
  try {
    await network.addKadNode(address.value.trim())
    ui.success('节点已加入 KAD 引导列表')
    address.value = ''
    showAdd.value = false
  } catch (cause) {
    ui.error(cause)
  } finally {
    updating.value = false
  }
}

function lastSeen(ms: number): string {
  if (!ms) return '—'
  return new Date(ms).toLocaleString('zh-CN', { hour12: false })
}

// eMule reads CT_EMULE_UDPPORTS from connected clients and calls
// CKademlia::Bootstrap(peerIP, kadUDPPort). The engine records those endpoints.
async function bootstrapFromPeers(): Promise<void> {
  updating.value = true
  try {
    await network.bootstrapKadFromPeers()
    ui.success(network.kad.message || '已从连接节点引导 KAD')
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
  <div class="view kad-view">
    <section class="panel">
      <header class="panel__head panel__head--toolbar">
        <div>
          <h2>KAD 节点</h2>
          <p>节点来自 nodes.dat 引导与 KAD 路由表；添加节点后立即参与引导</p>
        </div>
        <div class="toolbar-actions">
          <button class="btn btn--ghost" :disabled="updating || network.busy" @click="refresh">
            <Icon name="refresh" :size="15" /> 刷新
          </button>
          <button class="btn btn--ghost" :disabled="updating || network.busy || !network.kad.available"
            title="读取已连接客户端的 Kad UDP 端口并加入引导节点" @click="bootstrapFromPeers">
            <Icon name="link" :size="15" /> 从连接引导
          </button>
          <button class="btn btn--primary" @click="showAdd = !showAdd">
            <Icon :name="showAdd ? 'close' : 'plus'" :size="15" /> {{ showAdd ? '取消' : '添加节点' }}
          </button>
        </div>
      </header>

      <div v-if="showAdd" class="server-add-form">
        <label class="field field--full">
          <span class="field__label">节点地址</span>
          <input v-model="address" type="text" placeholder="例如 212.179.10.5:4662" />
          <span class="field__hint">按 host:port 格式填写 UDP 地址</span>
        </label>
        <div class="form-actions form-actions--end">
          <button class="btn btn--primary" :disabled="updating" @click="add">
            <Icon name="check" :size="15" /> 添加引导节点
          </button>
        </div>
      </div>

      <div class="ed-stats ed-stats--kad">
        <div class="ed-stat">
          <span>KAD 连接状态</span>
          <strong :class="network.kad.bootstrapped ? 'ed-stat__ok' : 'ed-stat__warn'">
            {{ !network.kad.available ? '未启用' : network.kad.bootstrapped ? '已联通' : '未收到应答' }}
          </strong>
        </div>
        <div class="ed-stat">
          <span>已应答 / 已加载节点</span>
          <strong>{{ network.kad.verifiedNodes }} / {{ network.kad.knownNodes }}</strong>
        </div>
        <div class="ed-stat">
          <span>存活 / 备用节点</span>
          <strong>{{ network.kad.liveNodes }} / {{ network.kad.replacementNodes }}</strong>
        </div>
        <div class="ed-stat">
          <span>路由节点</span>
          <strong>{{ network.kad.routerNodes }}</strong>
        </div>
        <div class="ed-stat">
          <span>防火墙状态</span>
          <strong>{{ network.kad.firewalled ? '受限（需穿透）' : '开放' }}</strong>
        </div>
        <div class="ed-stat">
          <span>UDP 监听端口</span>
          <strong>{{ network.kad.listenPort || '—' }}</strong>
        </div>
        <div class="ed-stat">
          <span>存储点</span>
          <strong class="ed-stat__text">{{ network.kad.storagePoint || '—' }}</strong>
        </div>
        <div class="ed-stat">
          <span>本节点 ID</span>
          <strong class="ed-stat__text mono">{{ network.kad.selfId || '—' }}</strong>
        </div>
        <div class="ed-stat">
          <span>进行中穿透</span>
          <strong>{{ network.kad.runningTraversals }}</strong>
        </div>
      </div>

      <div v-if="network.kad.message" class="notice notice--warn">
        <Icon name="alert" :size="15" />
        <span>{{ network.kad.message }}</span>
        <button class="link-btn" @click="router.push({ name: 'settings', query: { tab: 'emule' } })">前往设置</button>
      </div>

      <div v-if="network.kadLoading && !nodes.length" class="table-loading">正在读取 KAD 路由表…</div>
      <EmptyState
        v-else-if="!nodes.length"
        icon="wifi"
        title="KAD 路由表为空"
        hint="配置 nodes.dat 并启用 eD2k 引擎后，节点会随搜索与引导自动填充"
      />
      <div v-else class="task-table__wrap">
        <table class="task-table ed-table">
          <thead>
            <tr>
              <th>节点 ID</th>
              <th>地址</th>
              <th>TCP</th>
              <th>版本</th>
              <th>标记</th>
              <th>应答状态</th>
              <th>失败</th>
              <th>最后活跃</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="node in nodes" :key="node.address">
              <td class="mono">{{ node.id || '未知' }}</td>
              <td class="mono">{{ node.address }}</td>
              <td>{{ node.tcpPort || '—' }}</td>
              <td>{{ node.version || '—' }}</td>
              <td>
                <span v-if="node.router" class="chip chip--soft">路由</span>
                <span v-if="node.seed" class="chip chip--soft">种子</span>
                <span v-if="!node.router && !node.seed" class="muted-cell">—</span>
              </td>
              <td>
                <span v-if="node.responded" class="chip chip--success">已应答</span>
                <span v-else-if="node.pinged" class="chip chip--soft">路由存活</span>
                <span v-else-if="node.helloSent" class="chip chip--soft">Hello 已发</span>
                <span v-else class="muted-cell">未探测</span>
              </td>
              <td>{{ node.failCount }}</td>
              <td class="muted-cell">{{ lastSeen(node.lastSeen) }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </section>
  </div>
</template>
