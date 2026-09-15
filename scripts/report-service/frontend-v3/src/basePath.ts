// Runtime base path for the SPA.
//
// The Go server injects the configured REPORT_BASE_PATH into the <base href>
// element when it serves index.html (e.g. `<base href="/dashboard/">`, or
// `<base href="/">` at root). We read the prefix from that element rather than
// from an inline script: Vite's HTML transform (npm build in the Docker image)
// strips plain inline <script> tags, but the <base> tag survives — so <base>
// is the reliable source of truth.
//
// BASE_PATH never has a trailing slash: "" (root) or "/dashboard".
//
// Use withBase() for any URL that bypasses react-router — raw window.location
// assignments, <a href>, and fetch() targets. Router-driven navigation
// (<Link>, navigate()) already accounts for the router basename, so do NOT
// wrap those.

function readBasePath(): string {
  if (typeof document === 'undefined') return '';
  // getAttribute returns the raw attribute (e.g. "/dashboard/" or "/"), not a
  // resolved absolute URL like `.href` would.
  const href = document.querySelector('base')?.getAttribute('href') ?? '/';
  // Un-replaced placeholder (e.g. `vite dev` without the server injection).
  if (href.includes('__BASE_PATH__')) return '';
  return href.replace(/\/+$/, ''); // "/dashboard/" -> "/dashboard", "/" -> ""
}

export const BASE_PATH = readBasePath();

// Where the v3 SPA is mounted for THIS request. It is served two ways:
//   • under /v3 on hosts that still run the legacy UI at / (coexistence)
//   • at root (/) on hosts that show v3 as the whole site (e.g. gw.nexroute.cc)
// Detected from the current URL so router basename + hard-coded links resolve
// correctly in both cases. "" = root mount, "/v3" = prefixed mount.
export const APP_BASE =
  typeof window !== 'undefined' && window.location.pathname.startsWith('/v3') ? '/v3' : '';

export function withBase(path: string): string {
  if (!path.startsWith('/')) return path;
  return BASE_PATH + path;
}
