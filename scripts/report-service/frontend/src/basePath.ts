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

export function withBase(path: string): string {
  if (!path.startsWith('/')) return path;
  return BASE_PATH + path;
}
