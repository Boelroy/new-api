// Runtime base path for the SPA. The Go server injects the configured
// REPORT_BASE_PATH into `window.__BASE_PATH__` (via index.html) when it serves
// the shell. Empty (or the un-replaced placeholder in dev) means "mounted at
// root". BASE_PATH never has a trailing slash: "" or "/dashboard".
//
// Use withBase() for any URL that bypasses react-router — raw
// window.location assignments, <a href>, and fetch() targets. Router-driven
// navigation (<Link>, navigate()) already accounts for the router basename, so
// do NOT wrap those.

declare global {
  interface Window {
    __BASE_PATH__?: string;
  }
}

const raw = typeof window !== 'undefined' ? window.__BASE_PATH__ : '';
export const BASE_PATH =
  raw && raw !== '__BASE_PATH__' ? raw.replace(/\/$/, '') : '';

export function withBase(path: string): string {
  if (!path.startsWith('/')) return path;
  return BASE_PATH + path;
}
