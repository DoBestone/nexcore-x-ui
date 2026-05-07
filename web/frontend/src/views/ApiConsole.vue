<script setup lang="ts">
// API 控制台:展示 token 列表 + 创建 / 撤销 + 渲染 docs/api.md。
// 旧 panel 的 api_console.html 把 markdown 渲染、token CRUD、code preview 揉在
// 一个 700 行模板里;v2 拆成两个 tab。
import { onMounted, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Plus, Refresh } from '@element-plus/icons-vue'
import { http, get, post } from '@/api/http'
import type { ApiToken } from '@/api/types'

const tokens = ref<ApiToken[]>([])
const docs = ref('')
const loading = ref(false)

async function loadTokens() {
  loading.value = true
  try {
    tokens.value = (await get<ApiToken[]>('xui/api/tokens')) || []
  } finally {
    loading.value = false
  }
}

async function loadDocs() {
  try {
    const r = await http.get('xui/api/docs', { responseType: 'text' })
    docs.value = String(r.data || '')
  } catch {
    docs.value = '# API 文档加载失败'
  }
}

const createVisible = ref(false)
const newName = ref('')
const newScope = ref<'admin' | 'readonly'>('admin')
const newTtl = ref(0)
const issued = ref<{ name: string; token: string } | null>(null)

function openCreate() {
  newName.value = ''
  newScope.value = 'admin'
  newTtl.value = 0
  issued.value = null
  createVisible.value = true
}

async function doCreate() {
  if (!newName.value) {
    ElMessage.warning('名称必填')
    return
  }
  const r = await post<{ name: string; token: string }>('xui/api/tokens', {
    name: newName.value,
    scope: newScope.value,
    ttlSeconds: newTtl.value
  })
  issued.value = { name: r.name, token: r.token }
  await loadTokens()
}

async function revoke(t: ApiToken) {
  await post(`xui/api/tokens/${t.id}/revoke`)
  ElMessage.success('已撤销')
  await loadTokens()
}

async function delToken(t: ApiToken) {
  try {
    await ElMessageBox.confirm(`删除 token ${t.name}?`, '删除', {
      type: 'warning',
      confirmButtonText: '删除',
      cancelButtonText: '取消'
    })
  } catch {
    return
  }
  await http.delete(`xui/api/tokens/${t.id}`)
  ElMessage.success('已删除')
  await loadTokens()
}

function fmt(ms: number): string {
  if (!ms) return '-'
  return new Date(ms).toLocaleString()
}

async function copy(text: string) {
  try {
    await navigator.clipboard.writeText(text)
    ElMessage.success('已复制')
  } catch {
    ElMessage.warning('复制失败')
  }
}

onMounted(async () => {
  await Promise.all([loadTokens(), loadDocs()])
})
</script>

<template>
  <div class="nx-page">
    <h2>API 控制台</h2>

    <el-tabs>
      <el-tab-pane label="访问令牌" lazy>
        <div class="nx-row" style="margin-bottom: 12px">
          <el-button type="primary" :icon="Plus" @click="openCreate">创建 token</el-button>
          <el-button :icon="Refresh" @click="loadTokens" :loading="loading">刷新</el-button>
        </div>
        <el-card>
          <el-empty v-if="tokens.length === 0" description="还没有 API token" />
          <el-table v-else :data="tokens" stripe>
            <el-table-column prop="id" label="id" width="60" />
            <el-table-column prop="name" label="名称" min-width="160" />
            <el-table-column prop="scope" label="权限" width="120">
              <template #default="{ row }">
                <el-tag :type="row.scope === 'admin' ? 'danger' : 'info'">{{ row.scope }}</el-tag>
              </template>
            </el-table-column>
            <el-table-column label="创建时间" width="180">
              <template #default="{ row }">{{ fmt(row.createdAt) }}</template>
            </el-table-column>
            <el-table-column label="过期时间" width="180">
              <template #default="{ row }">
                <span v-if="row.expiresAt > 0">{{ fmt(row.expiresAt) }}</span>
                <el-tag v-else type="success">永不过期</el-tag>
              </template>
            </el-table-column>
            <el-table-column label="最后使用" width="180">
              <template #default="{ row }">{{ fmt(row.lastUsedAt) }}</template>
            </el-table-column>
            <el-table-column label="操作" width="160" align="right">
              <template #default="{ row }">
                <el-button size="small" @click="revoke(row)">撤销</el-button>
                <el-button size="small" type="danger" plain @click="delToken(row)">删除</el-button>
              </template>
            </el-table-column>
          </el-table>
        </el-card>
      </el-tab-pane>

      <el-tab-pane label="API 文档" lazy>
        <el-card>
          <pre class="md">{{ docs }}</pre>
        </el-card>
      </el-tab-pane>
    </el-tabs>

    <el-dialog v-model="createVisible" title="创建 API token" width="480px" class="constrained-dialog" :align-center="false">
      <div v-if="!issued">
        <el-form label-width="100px" label-position="left">
          <el-form-item label="名称">
            <el-input v-model="newName" />
          </el-form-item>
          <el-form-item label="权限">
            <el-select v-model="newScope">
              <el-option value="admin" label="admin (读写)" />
              <el-option value="readonly" label="readonly (只读)" />
            </el-select>
          </el-form-item>
          <el-form-item label="有效时长">
            <el-input-number v-model="newTtl" :min="0" />
            <span class="nx-muted" style="margin-left: 8px">秒,0 = 永不过期</span>
          </el-form-item>
        </el-form>
      </div>
      <div v-else>
        <el-alert type="warning" :closable="false" show-icon>
          <template #title>token 只在此处显示一次,请立即保存</template>
        </el-alert>
        <div class="issued nx-mono">{{ issued.token }}</div>
        <el-button size="small" type="primary" @click="copy(issued.token)">复制</el-button>
      </div>
      <template #footer>
        <el-button @click="createVisible = false">{{ issued ? '关闭' : '取消' }}</el-button>
        <el-button v-if="!issued" type="primary" @click="doCreate">创建</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped>
.md {
  white-space: pre-wrap;
  word-break: break-word;
  font-family: ui-monospace, monospace;
  font-size: 12.5px;
  color: var(--nx-text);
  margin: 0;
}
.issued {
  background: var(--nx-bg);
  padding: 12px;
  border-radius: 6px;
  word-break: break-all;
  margin: 12px 0;
  border: 1px solid var(--nx-border);
  font-size: 12px;
}
</style>
