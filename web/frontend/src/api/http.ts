import axios, { type AxiosRequestConfig } from 'axios'
import { ElMessage } from 'element-plus'
import { basePath } from '@/utils/base'

// SPA 与面板同源,cookie 直接随请求走;dev 模式 vite proxy 把
// /xui /api /login /logout /server 转到本地 panel 端口。
//
// baseURL 用 basePath(末尾保证有 '/'),所有调用点写相对路径(无前导 /),
// 这样即便面板部署在 /admin/ 这种非根 base path 下也能正确解析。
export const http = axios.create({
  baseURL: basePath,
  withCredentials: true,
  timeout: 30000,
  headers: {
    'X-Requested-With': 'XMLHttpRequest'
  }
})

http.interceptors.response.use(
  (resp) => resp,
  (err) => {
    if (err.response?.status === 401) {
      // 401 跳登录,但要避免在登录页 / magic-login 上递归跳转
      const here = location.pathname
      const loginPath = basePath + 'login'
      const magicPath = basePath + 'magic-login'
      if (here !== loginPath && here !== magicPath) {
        location.replace(loginPath)
      }
    }
    return Promise.reject(err)
  }
)

// 后端约定:{ success, msg, obj } 格式。Helper 把它解开成更直观的形态。
export interface PanelMsg<T = unknown> {
  success: boolean
  msg?: string
  obj?: T
}

export async function call<T = unknown>(
  method: 'get' | 'post' | 'put' | 'delete' | 'patch',
  url: string,
  data?: unknown,
  cfg: AxiosRequestConfig = {}
): Promise<T> {
  const r = await http.request<PanelMsg<T>>({ method, url, data, ...cfg })
  if (r.data && typeof r.data === 'object' && 'success' in r.data) {
    if (!r.data.success) {
      ElMessage.error(r.data.msg || '请求失败')
      throw new Error(r.data.msg || 'request failed')
    }
    return (r.data.obj ?? (undefined as unknown)) as T
  }
  return r.data as unknown as T
}

export const get = <T = unknown>(url: string, cfg?: AxiosRequestConfig) =>
  call<T>('get', url, undefined, cfg)
export const post = <T = unknown>(url: string, data?: unknown, cfg?: AxiosRequestConfig) =>
  call<T>('post', url, data, cfg)
export const put = <T = unknown>(url: string, data?: unknown, cfg?: AxiosRequestConfig) =>
  call<T>('put', url, data, cfg)
export const del = <T = unknown>(url: string, cfg?: AxiosRequestConfig) =>
  call<T>('delete', url, undefined, cfg)

// 部分老接口(/login /xui/inbound/list)默认只接受 form-urlencoded。
// 用 postForm 走 URLSearchParams,避开全局 JSON header。
export async function postForm<T = unknown>(url: string, data: Record<string, unknown> = {}) {
  const body = new URLSearchParams()
  for (const [k, v] of Object.entries(data)) {
    if (v === undefined || v === null) continue
    body.append(k, typeof v === 'string' ? v : JSON.stringify(v))
  }
  const r = await http.post<PanelMsg<T>>(url, body, {
    headers: { 'Content-Type': 'application/x-www-form-urlencoded' }
  })
  if (r.data && typeof r.data === 'object' && 'success' in r.data) {
    if (!r.data.success) {
      ElMessage.error(r.data.msg || '请求失败')
      throw new Error(r.data.msg || 'request failed')
    }
    return (r.data.obj ?? (undefined as unknown)) as T
  }
  return r.data as unknown as T
}
