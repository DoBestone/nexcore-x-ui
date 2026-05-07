<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Plus, MagicStick, Refresh } from '@element-plus/icons-vue'
import { postForm } from '@/api/http'
import type { BlockRule } from '@/api/types'

const rules = ref<BlockRule[]>([])
const presets = ref<{ key: string; label: string; description?: string }[]>([])
const loading = ref(false)

async function reload() {
  loading.value = true
  try {
    rules.value = (await postForm<BlockRule[]>('xui/block-rule/list')) || []
    presets.value =
      (await postForm<{ key: string; label: string; description?: string }[]>(
        'xui/block-rule/presets'
      )) || []
  } finally {
    loading.value = false
  }
}

const formVisible = ref(false)
const editing = ref<Partial<BlockRule>>({
  type: 'domain',
  value: '',
  remark: '',
  inboundTag: '',
  enable: true
})

function openAdd() {
  editing.value = { type: 'domain', value: '', remark: '', inboundTag: '', enable: true }
  formVisible.value = true
}

function openEdit(r: BlockRule) {
  editing.value = { ...r }
  formVisible.value = true
}

async function save() {
  const data = {
    type: editing.value.type || 'domain',
    value: editing.value.value || '',
    remark: editing.value.remark || '',
    inboundTag: editing.value.inboundTag || '',
    enable: editing.value.enable ?? true
  }
  if (!data.value) {
    ElMessage.warning('值必填')
    return
  }
  if (editing.value.id) {
    await postForm(`xui/block-rule/update/${editing.value.id}`, data)
  } else {
    await postForm('xui/block-rule/add', data)
  }
  ElMessage.success('已保存')
  formVisible.value = false
  await reload()
}

async function del(r: BlockRule) {
  try {
    await ElMessageBox.confirm(`确认删除规则 ${r.type}: ${r.value}?`, '删除', {
      type: 'warning',
      confirmButtonText: '删除',
      cancelButtonText: '取消'
    })
  } catch {
    return
  }
  await postForm(`xui/block-rule/del/${r.id}`)
  ElMessage.success('已删除')
  await reload()
}

async function toggle(r: BlockRule, v: boolean) {
  await postForm(`xui/block-rule/toggle/${r.id}`, { enable: v })
  r.enable = v
}

const presetVisible = ref(false)
const presetKey = ref('')
const presetTag = ref('')

function openPreset() {
  presetKey.value = presets.value[0]?.key || ''
  presetTag.value = ''
  presetVisible.value = true
}

async function applyPreset() {
  if (!presetKey.value) {
    ElMessage.warning('请选择预置')
    return
  }
  await postForm('xui/block-rule/apply-preset', { key: presetKey.value, inboundTag: presetTag.value })
  ElMessage.success('已应用预置')
  presetVisible.value = false
  await reload()
}

onMounted(() => reload())
</script>

<template>
  <div class="nx-page">
    <h2>屏蔽规则</h2>

    <div class="nx-row" style="margin-bottom: 16px">
      <el-button type="primary" :icon="Plus" @click="openAdd">新增规则</el-button>
      <el-button :icon="MagicStick" @click="openPreset">应用预置</el-button>
      <el-button :icon="Refresh" @click="reload" :loading="loading">刷新</el-button>
    </div>

    <el-card>
      <el-empty v-if="rules.length === 0" description="暂无屏蔽规则" />
      <el-table v-else :data="rules" stripe>
        <el-table-column prop="id" label="id" width="60" />
        <el-table-column prop="type" label="类型" width="120" />
        <el-table-column prop="value" label="值" min-width="200" show-overflow-tooltip />
        <el-table-column prop="remark" label="备注" min-width="160" />
        <el-table-column prop="inboundTag" label="作用入站">
          <template #default="{ row }">
            <span v-if="row.inboundTag">{{ row.inboundTag }}</span>
            <el-tag v-else type="info" size="small">全局</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="启用" width="80">
          <template #default="{ row }">
            <el-switch :model-value="row.enable" @change="(v: boolean) => toggle(row, v)" />
          </template>
        </el-table-column>
        <el-table-column label="操作" width="160" align="right">
          <template #default="{ row }">
            <el-button size="small" @click="openEdit(row)">编辑</el-button>
            <el-button size="small" type="danger" plain @click="del(row)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>
    </el-card>

    <el-dialog v-model="formVisible" :title="editing.id ? '编辑规则' : '新增规则'" width="520px" class="constrained-dialog" :align-center="true">
      <el-form label-width="100px" label-position="left">
        <el-form-item label="类型">
          <el-select v-model="editing.type">
            <el-option value="domain" label="domain" />
            <el-option value="ip" label="ip" />
            <el-option value="geosite" label="geosite" />
            <el-option value="geoip" label="geoip" />
            <el-option value="port" label="port" />
            <el-option value="protocol" label="protocol" />
            <el-option value="source" label="source" />
          </el-select>
        </el-form-item>
        <el-form-item label="值">
          <el-input v-model="editing.value" placeholder="逗号分隔多个值" />
        </el-form-item>
        <el-form-item label="备注">
          <el-input v-model="editing.remark" />
        </el-form-item>
        <el-form-item label="作用入站">
          <el-input v-model="editing.inboundTag" placeholder="留空 = 全局" />
        </el-form-item>
        <el-form-item label="启用">
          <el-switch v-model="editing.enable" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="formVisible = false">取消</el-button>
        <el-button type="primary" @click="save">保存</el-button>
      </template>
    </el-dialog>

    <el-dialog v-model="presetVisible" title="应用屏蔽预置" width="480px" class="constrained-dialog" :align-center="true">
      <el-form label-width="100px" label-position="left">
        <el-form-item label="预置">
          <el-select v-model="presetKey">
            <el-option v-for="p in presets" :key="p.key" :value="p.key" :label="p.label || p.key" />
          </el-select>
          <div v-if="presetKey" class="nx-muted" style="margin-top: 4px">
            {{ presets.find((p) => p.key === presetKey)?.description }}
          </div>
        </el-form-item>
        <el-form-item label="作用入站">
          <el-input v-model="presetTag" placeholder="留空 = 全局" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="presetVisible = false">取消</el-button>
        <el-button type="primary" @click="applyPreset">应用</el-button>
      </template>
    </el-dialog>
  </div>
</template>
