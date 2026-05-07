<script setup lang="ts">
import { ref, watch } from 'vue'
import QRCode from 'qrcode'
import { ElMessage } from 'element-plus'

const props = defineProps<{
  modelValue: boolean
  title: string
  link: string
}>()
const emit = defineEmits<{ (e: 'update:modelValue', v: boolean): void }>()

const dataUrl = ref('')

watch(
  () => [props.modelValue, props.link] as const,
  async ([open, link]) => {
    if (open && link) {
      dataUrl.value = await QRCode.toDataURL(link, { width: 280, margin: 1 })
    } else {
      dataUrl.value = ''
    }
  },
  { immediate: true }
)

async function copy() {
  try {
    await navigator.clipboard.writeText(props.link)
    ElMessage.success('已复制')
  } catch {
    ElMessage.warning('复制失败,请手动选择文本')
  }
}
</script>

<template>
  <el-dialog
    :model-value="modelValue"
    @update:model-value="(v: boolean) => emit('update:modelValue', v)"
    :title="title || '二维码'"
    width="360px"
    :close-on-click-modal="true"
  >
    <div class="qr-wrap">
      <img v-if="dataUrl" :src="dataUrl" alt="qrcode" />
      <div v-else class="qr-placeholder">生成中...</div>
      <div class="qr-link">{{ link }}</div>
      <el-button size="small" type="primary" @click="copy">复制链接</el-button>
    </div>
  </el-dialog>
</template>

<style scoped>
.qr-wrap {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 12px;
}
.qr-wrap img {
  width: 280px;
  height: 280px;
  border-radius: 8px;
  background: #fff;
}
.qr-placeholder {
  width: 280px;
  height: 280px;
  display: flex;
  align-items: center;
  justify-content: center;
  background: var(--nx-bg);
  border-radius: 8px;
  color: var(--nx-text-muted);
}
.qr-link {
  font-family: ui-monospace, monospace;
  font-size: 12px;
  color: var(--nx-text-muted);
  word-break: break-all;
  text-align: center;
  background: var(--nx-bg);
  padding: 8px;
  border-radius: 6px;
  width: 100%;
  max-height: 80px;
  overflow-y: auto;
}
</style>
