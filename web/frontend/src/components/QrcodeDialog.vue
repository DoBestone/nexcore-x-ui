<script setup lang="ts">
import { ref, watch } from 'vue'
import { ElMessage } from 'element-plus'

// qrcode 是 ~50KB 的二维码生成库;面板大多数页面不会触发它(只在
// Inbounds → 链接 → 二维码弹层里才需要)。改成 dynamic import 之后,
// QrcodeDialog 第一次打开时才会拉这个 chunk,首屏体积下降一档。

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
      const QRCode = (await import('qrcode')).default
      dataUrl.value = await QRCode.toDataURL(link, { width: 280, margin: 1 })
    } else {
      dataUrl.value = ''
    }
  },
  { immediate: true }
)

async function copy() {
  // navigator.clipboard 只在 HTTPS / localhost 上下文可用。X-UI 面板典型部署
  // 是 http://IP:port 直连,modern API 会被浏览器以 NotAllowedError 拒掉 →
  // fallback 到老 textarea + execCommand,虽然 deprecated 但在 http 上下文里
  // 还能跑。两条路都失败才提示用户手选。
  const text = props.link
  try {
    if (window.isSecureContext && navigator.clipboard) {
      await navigator.clipboard.writeText(text)
      ElMessage.success('已复制')
      return
    }
  } catch {
    /* fall through 到老方法 */
  }
  try {
    const ta = document.createElement('textarea')
    ta.value = text
    ta.setAttribute('readonly', '')
    ta.style.position = 'fixed'
    ta.style.top = '0'
    ta.style.left = '0'
    ta.style.opacity = '0'
    document.body.appendChild(ta)
    ta.focus()
    ta.select()
    const ok = document.execCommand('copy')
    document.body.removeChild(ta)
    ElMessage[ok ? 'success' : 'warning'](ok ? '已复制' : '复制失败,请手动选中链接复制')
  } catch {
    ElMessage.warning('复制失败,请手动选中链接复制')
  }
}
</script>

<template>
  <el-dialog
    :model-value="modelValue"
    @update:model-value="(v: boolean) => emit('update:modelValue', v)"
    :title="title || '二维码'"
    width="360px"
    class="constrained-dialog"
    :align-center="false"
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
