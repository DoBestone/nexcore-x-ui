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

const PLACEHOLDER = '__NX_BASE__'

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
