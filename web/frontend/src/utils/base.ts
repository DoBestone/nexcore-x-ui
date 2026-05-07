// resolveBasePath returns the panel's base path (always with leading
// AND trailing slash). The Go server injects window.__NX_BASE__ into
// index.html so axios + vue-router agree on where the panel is mounted.
//
// During Vite dev (no SSR injection), the placeholder string survives
// verbatim — we treat that as "no override" and fall back to "/".
//
// Trailing slash discipline matters:
//   - axios baseURL must end in "/" so relative path "xui/api/me"
//     concatenates to "{base}/xui/api/me" rather than "{base}xui/api/me".
//   - vue-router createWebHistory base also expects trailing slash.
//
// We collapse multiple slashes / strip duplicate trailing slashes, but
// don't validate the operator-set value beyond that — the server
// already validated it before it landed in window.__NX_BASE__.

declare global {
  interface Window {
    __NX_BASE__?: string
  }
}

// 占位符跟变量名 __NX_BASE__ 区分,否则 server 端 ReplaceAll 会把
// 变量名也替换掉破坏 JS 语法。dev 模式下 index.html 没经过 server 注入,
// window.__NX_BASE__ 拿到的是字面 "%%NX_BASE%%",我们识别为"没覆盖"退化到 /。
const PLACEHOLDER = '%%NX_BASE%%'

export function resolveBasePath(): string {
  const raw = (window.__NX_BASE__ ?? '').toString()
  // Vite dev preview: index.html still has the literal placeholder.
  if (!raw || raw === PLACEHOLDER) return '/'
  let v = raw.trim()
  if (!v.startsWith('/')) v = '/' + v
  if (!v.endsWith('/')) v = v + '/'
  // Collapse runs of '/' to a single slash so accidentally-doubled
  // operator config doesn't break URL composition.
  return v.replace(/\/+/g, '/')
}

export const basePath = resolveBasePath()
