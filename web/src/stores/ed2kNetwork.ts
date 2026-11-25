import { defineStore } from 'pinia'
import { ref } from 'vue'
import { rpc } from '@/api/rpc'
import type { ED2KKadStatus, ED2KServersStatus } from '@/types/api'

// Server list / KAD state is polled only while the corresponding view is open.
export const useED2KNetworkStore = defineStore('ed2kNetwork', () => {
  const servers = ref<ED2KServersStatus>({
    available: false, engineStarted: false, serverMet: '', connected: '',
    servers: [], message: '',
  })
  const kad = ref<ED2KKadStatus>({
    available: false, engineStarted: false, bootstrapped: false, firewalled: false,
    liveNodes: 0, replacementNodes: 0, routerNodes: 0, runningTraversals: 0,
    knownNodes: 0, verifiedNodes: 0, listenPort: 0, storagePoint: '', selfId: '', nodes: [], message: '',
  })
  const serversLoading = ref(false)
  const kadLoading = ref(false)
  const busy = ref(false)

  async function refreshServers(): Promise<void> {
    serversLoading.value = true
    try {
      servers.value = await rpc.ed2kNetwork.servers()
    } finally {
      serversLoading.value = false
    }
  }

  async function refreshKad(): Promise<void> {
    kadLoading.value = true
    try {
      kad.value = await rpc.ed2kNetwork.kad()
    } finally {
      kadLoading.value = false
    }
  }

  async function addServer(address: string, name: string, description: string): Promise<void> {
    busy.value = true
    try {
      servers.value = await rpc.ed2kNetwork.addServer({ address, name, description })
    } finally {
      busy.value = false
    }
  }

  async function removeServer(address: string): Promise<void> {
    busy.value = true
    try {
      servers.value = await rpc.ed2kNetwork.removeServer(address)
    } finally {
      busy.value = false
    }
  }

  async function connectServer(address: string): Promise<void> {
    busy.value = true
    try {
      servers.value = await rpc.ed2kNetwork.connectServer(address)
    } finally {
      busy.value = false
    }
  }

  async function disconnectServers(): Promise<void> {
    busy.value = true
    try {
      servers.value = await rpc.ed2kNetwork.disconnectServers()
    } finally {
      busy.value = false
    }
  }

  async function addKadNode(address: string): Promise<void> {
    busy.value = true
    try {
      kad.value = await rpc.ed2kNetwork.addKadNode(address)
    } finally {
      busy.value = false
    }
  }

  async function updateServersFromPeers(): Promise<void> {
    busy.value = true
    try {
      servers.value = await rpc.ed2kNetwork.updateServersFromPeers()
    } finally {
      busy.value = false
    }
  }

  async function bootstrapKadFromPeers(): Promise<void> {
    busy.value = true
    try {
      kad.value = await rpc.ed2kNetwork.bootstrapKadFromPeers()
    } finally {
      busy.value = false
    }
  }

  return {
    servers, kad, serversLoading, kadLoading, busy,
    refreshServers, refreshKad, addServer, removeServer, connectServer,
    disconnectServers, addKadNode, updateServersFromPeers, bootstrapKadFromPeers,
  }
})
