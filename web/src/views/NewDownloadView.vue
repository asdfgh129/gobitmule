<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import Icon from '@/components/Icon.vue'
import EmptyState from '@/components/EmptyState.vue'
import { rpc } from '@/api/rpc'
import { useSessionStore } from '@/stores/session'
import { useUiStore } from '@/stores/ui'
import { formatBytes } from '@/utils/format'
import type { Resource, TaskOptions } from '@/types/api'

const router = useRouter()
const session = useSessionStore()
const ui = useUiStore()

const input = ref('')
const advanced = ref(false)
const submitting = ref(false)
const previewing = ref(false)
const preview = ref<Resource | null>(null)
const previewError = ref('')
const secret = ref('')
const credentialRef = ref('')
const savingSecret = ref(false)

const form = ref<TaskOptions>({
  dir: '', out: '', maxConnections: 0, maxDownloadLimit: '',
  priority: 0, postProcess: '',
})

const lines = computed(() => input.value.split('\n').map((s) => s.trim()).filter(Boolean))
const isBatch = computed(() => lines.value.length > 1)
const canPreview = computed(() => lines.value.length === 1 && /^(https?:|ftps?:|sftp:|magnet:|thunder:|ed2k:)/i.test(lines.value[0] ?? ''))
const postProcessors = computed(() => session.capabilities?.postProcessors ?? ['none'])

let debounce: ReturnType<typeof setTimeout> | null = null
watch(input, () => {
  preview.value = null
  previewError.value = ''
  if (debounce) clearTimeout(debounce)
  if (!canPreview.value) return
  debounce = setTimeout(() => void runPreview(), 600)
})

async function runPreview(): Promise<void> {
  if (!canPreview.value) return
  previewing.value = true
  previewError.value = ''
  try {
    preview.value = await rpc.tasks.preview(lines.value[0], cleanOptions())
  } catch (cause) {
    preview.value = null
    previewError.value = cause instanceof Error ? cause.message : String(cause)
  } finally {
    previewing.value = false
  }
}

function cleanOptions(): TaskOptions {
  const result: TaskOptions = {}
  const o = form.value
  if (o.dir?.trim()) result.dir = o.dir.trim()
  if (o.out?.trim()) result.out = o.out.trim()
  if (o.maxConnections && o.maxConnections > 0) result.maxConnections = o.maxConnections
  if (o.maxDownloadLimit?.trim()) result.maxDownloadLimit = o.maxDownloadLimit.trim()
  if (o.priority) result.priority = o.priority
  if (o.postProcess) result.postProcess = o.postProcess
  if (credentialRef.value) result.credentialRef = credentialRef.value
  return result
}

async function saveSecret(): Promise<void> {
  if (!secret.value.trim()) return
  savingSecret.value = true
  try {
    const result = await rpc.credentials.put(secret.value)
    credentialRef.value = result.credentialRef
    secret.value = ''
    ui.success('凭据已写入安全存储，仅在任务中保存引用')
  } catch (cause) {
    ui.error(cause)
  } finally {
    savingSecret.value = false
  }
}

async function submit(): Promise<void> {
  if (!lines.value.length) return
  submitting.value = true
  try {
    if (isBatch.value) {
      const result = await rpc.tasks.batch(lines.value, cleanOptions())
      ui.success(`已加入 ${result.count} 个任务`)
    } else {
      const result = await rpc.tasks.create(lines.value[0], cleanOptions(), '', 1200)
      ui.success(result.idempotent ? '该任务已存在' : '任务已创建')
    }
    input.value = ''
    preview.value = null
    await router.push({ name: 'tasks' })
  } catch (cause) {
    ui.error(cause)
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <div class="view create-view">
    <section class="panel create-form">
      <header class="panel__head">
        <div>
          <h2>添加下载链接</h2>
          <p>支持直链、种子、磁力链接、Metalink 与流媒体清单；每行一个可批量提交</p>
        </div>
      </header>

      <div class="form-body">
        <label class="field">
          <span class="field__label">下载链接 <em>*</em></span>
          <textarea
            v-model="input"
            rows="5"
            spellcheck="false"
            placeholder="https://example.com/file.iso&#10;magnet:?xt=urn:btih:..."
          />
          <span class="field__hint">{{ lines.length }} 行 · {{ isBatch ? '将批量创建' : '单个任务' }}</span>
        </label>

        <button class="disclosure" @click="advanced = !advanced">
          <Icon name="settings" :size="15" />
          {{ advanced ? '收起高级选项' : '展开高级选项' }}
          <Icon name="chevron" :size="14" :class="{ rotated: advanced }" />
        </button>

        <div v-if="advanced" class="advanced-grid">
          <label class="field">
            <span class="field__label">保存目录</span>
            <input v-model="form.dir" placeholder="留空使用默认下载目录" />
          </label>
          <label class="field">
            <span class="field__label">输出文件名</span>
            <input v-model="form.out" placeholder="留空使用源文件名" />
          </label>
          <label class="field">
            <span class="field__label">单任务连接数</span>
            <input v-model.number="form.maxConnections" type="number" min="0" max="256" />
          </label>
          <label class="field">
            <span class="field__label">单任务限速</span>
            <input v-model="form.maxDownloadLimit" placeholder="例如 2MiB/s" />
          </label>
          <label class="field">
            <span class="field__label">优先级</span>
            <input v-model.number="form.priority" type="number" />
          </label>
          <label class="field">
            <span class="field__label">后处理</span>
            <select v-model="form.postProcess">
              <option value="">不处理（none）</option>
              <option v-for="p in postProcessors.filter((x) => x !== 'none')" :key="p" :value="p">{{ p }}</option>
            </select>
          </label>
          <div class="field field--span">
            <span class="field__label">访问凭据（可选）</span>
            <div class="inline-field">
              <input v-model="secret" type="password" placeholder="仅写入系统安全存储，不随任务保存明文" />
              <button class="btn btn--ghost btn--sm" :disabled="!secret.trim() || savingSecret" @click="saveSecret">
                保存引用
              </button>
            </div>
            <span v-if="credentialRef" class="field__hint ok">已关联 {{ credentialRef }}</span>
          </div>
        </div>

        <div class="form-actions">
          <button class="btn btn--ghost" :disabled="!canPreview || previewing" @click="runPreview">
            <Icon name="search" :size="15" /> {{ previewing ? '解析中…' : '仅解析预览' }}
          </button>
          <button class="btn btn--primary" :disabled="!lines.length || submitting" @click="submit">
            <Icon name="plus" :size="16" /> {{ submitting ? '提交中…' : isBatch ? `批量添加 ${lines.length} 项` : '加入下载队列' }}
          </button>
        </div>
      </div>
    </section>

    <section class="panel preview-panel">
      <header class="panel__head">
        <div>
          <h2>解析预览</h2>
          <p>只读操作，不会创建任务</p>
        </div>
        <span v-if="preview" class="chip chip--success">已解析</span>
      </header>

      <div v-if="!preview && !previewError" class="preview-empty">
        <EmptyState icon="link" title="输入链接后自动预览" hint="预览展示解析器归类、文件清单与镜像，不产生任何写入。" />
      </div>
      <div v-else-if="previewError" class="notice notice--danger">
        <Icon name="alert" :size="17" />
        <div><strong>解析失败</strong><p>{{ previewError }}</p></div>
      </div>
      <div v-else-if="preview" class="preview-body">
        <div class="kv-grid">
          <div class="kv"><span>资源类型</span><strong>{{ preview.kind }}</strong></div>
          <div class="kv"><span>执行引擎</span><strong>{{ preview.engine }}</strong></div>
          <div class="kv"><span>镜像数</span><strong>{{ preview.mirrors.length }}</strong></div>
          <div class="kv"><span>文件数</span><strong>{{ preview.files.length }}</strong></div>
        </div>
        <h3 class="subhead">文件</h3>
        <ul class="file-list">
          <li v-for="file in preview.files" :key="file.index">
            <Icon name="file" :size="15" />
            <span class="file-list__name">{{ file.path }}</span>
            <span class="mono muted">{{ file.size ? formatBytes(file.size) : '未知' }}</span>
          </li>
        </ul>
        <h3 class="subhead">协议标识</h3>
        <pre class="code-block">{{ JSON.stringify(preview.identity, null, 2) }}</pre>
      </div>
    </section>
  </div>
</template>
