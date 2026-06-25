#!/usr/bin/env node
/**
 * Service stability probe — complements probe.mjs (capability snapshot) by
 * measuring distribution of latency / success rate / error classes across
 * repeated identical requests.
 *
 * Per model: N iterations × {non-streaming short, streaming short}, sequential
 * with --gap-ms pause between iterations. Outputs per-iteration JSON + a
 * short markdown summary at stability-reports/<host>-<model>-<date>.{json,md}.
 *
 * Stdlib only. Requires Node >= 18.
 */
import { existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { argv, env, exit } from "node:process";

function loadEnvFile() {
  const here = dirname(fileURLToPath(import.meta.url));
  const path = resolve(here, ".env");
  if (!existsSync(path)) return;
  for (const raw of readFileSync(path, "utf8").split("\n")) {
    const line = raw.trim();
    if (!line || line.startsWith("#")) continue;
    const eq = line.indexOf("=");
    if (eq === -1) continue;
    const k = line.slice(0, eq).trim();
    let v = line.slice(eq + 1).trim();
    if ((v.startsWith('"') && v.endsWith('"')) || (v.startsWith("'") && v.endsWith("'")))
      v = v.slice(1, -1);
    if (!(k in env)) env[k] = v;
  }
}

function parseArgs() {
  loadEnvFile();
  const a = {
    url: env.URL ?? null,
    key: env.KEY ?? null,
    model: env.MODEL ?? null,
    iterations: "20",
    "gap-ms": "1000",
    timeout: "30000",
    out: null,
  };
  for (let i = 2; i < argv.length; i++) {
    const tok = argv[i];
    if (tok === "-h" || tok === "--help") {
      console.error(
        "Usage: stability-probe.mjs [--url URL] [--key KEY] [--model MODEL] [--iterations N] [--gap-ms MS] [--timeout MS] [--out PATH]",
      );
      exit(0);
    }
    if (!tok.startsWith("--")) {
      console.error(`unknown positional: ${tok}`);
      exit(1);
    }
    const k = tok.slice(2);
    if (!(k in a)) {
      console.error(`unknown flag: --${k}`);
      exit(1);
    }
    a[k] = argv[++i];
  }
  for (const r of ["url", "key", "model"]) {
    if (!a[r]) {
      console.error(`missing --${r} (or ${r.toUpperCase()} in .env)`);
      exit(1);
    }
  }
  return {
    url: a.url,
    key: a.key,
    model: a.model,
    iterations: Math.max(1, Number(a.iterations) || 20),
    gapMs: Math.max(0, Number(a["gap-ms"]) || 0),
    timeoutMs: Number(a.timeout) || 30000,
    out: a.out,
  };
}

// ---- HTTP helpers (adapted from probe.mjs) ---------------------------------

function classifyError(status, bodyText) {
  if (status === 0) {
    if (bodyText.startsWith("<timeout")) return "timeout";
    return "network";
  }
  if (status >= 200 && status < 300) return "ok";
  if (status === 524) return "cf_524";
  if (status === 502 || status === 503 || status === 504) return "5xx_gateway";
  if (status >= 500) return "5xx_other";
  if (status === 429) return "429_rate_limit";
  if (status === 401 || status === 403) return "auth";
  if (status >= 400) return "4xx_other";
  return "unknown";
}

async function nonstreamProbe(url, key, model, timeoutMs) {
  const t0 = performance.now();
  let ttfbMs = null;
  let status = 0;
  let bodyText = "";
  let errMsg = null;
  const ctrl = new AbortController();
  const timer = setTimeout(() => ctrl.abort(), timeoutMs);
  try {
    const r = await fetch(`${url}/v1/messages`, {
      method: "POST",
      headers: {
        "x-api-key": key,
        authorization: `Bearer ${key}`,
        "anthropic-version": "2023-06-01",
        "content-type": "application/json",
      },
      body: JSON.stringify({
        model,
        max_tokens: 10,
        messages: [{ role: "user", content: "Reply with exactly the word PONG and nothing else." }],
      }),
      signal: ctrl.signal,
    });
    ttfbMs = Math.round(performance.now() - t0);
    status = r.status;
    bodyText = await r.text();
  } catch (e) {
    errMsg = e.name === "AbortError" ? `<timeout after ${timeoutMs}ms>` : `<network: ${e.message ?? e}>`;
    bodyText = errMsg;
  } finally {
    clearTimeout(timer);
  }
  const elapsedMs = Math.round(performance.now() - t0);
  let outputTokens = null;
  let contentText = null;
  if (status >= 200 && status < 300) {
    try {
      const j = JSON.parse(bodyText);
      outputTokens = j.usage?.output_tokens ?? null;
      contentText = j.content?.[0]?.text ?? null;
    } catch {}
  }
  return {
    kind: "nonstream",
    status,
    elapsedMs,
    ttfbMs,
    errorClass: classifyError(status, bodyText),
    outputTokens,
    contentText,
    bodySnippet: bodyText.slice(0, 240),
  };
}

async function streamProbe(url, key, model, timeoutMs) {
  const t0 = performance.now();
  let ttfbMs = null;
  let ttftMs = null;
  let status = 0;
  let maxGapMs = 0;
  let lastByteAt = performance.now();
  let outputTokens = 0;
  let body = "";
  let errMsg = null;
  const ctrl = new AbortController();
  const timer = setTimeout(() => ctrl.abort(), timeoutMs);
  try {
    const r = await fetch(`${url}/v1/messages`, {
      method: "POST",
      headers: {
        "x-api-key": key,
        authorization: `Bearer ${key}`,
        "anthropic-version": "2023-06-01",
        "content-type": "application/json",
      },
      body: JSON.stringify({
        model,
        max_tokens: 100,
        stream: true,
        messages: [{ role: "user", content: "Count from 1 to 20, separated by spaces." }],
      }),
      signal: ctrl.signal,
    });
    ttfbMs = Math.round(performance.now() - t0);
    status = r.status;
    if (r.body) {
      const reader = r.body.getReader();
      const decoder = new TextDecoder();
      let chunkIdx = 0;
      while (true) {
        const { value, done } = await reader.read();
        if (done) break;
        const now = performance.now();
        const text = decoder.decode(value, { stream: true });
        body += text;
        if (chunkIdx > 0) {
          const gap = now - lastByteAt;
          if (gap > maxGapMs) maxGapMs = gap;
        }
        lastByteAt = now;
        if (ttftMs === null && text.includes("content_block_delta")) {
          ttftMs = Math.round(now - t0);
        }
        const m = text.match(/"output_tokens"\s*:\s*(\d+)/);
        if (m) outputTokens = parseInt(m[1], 10);
        chunkIdx++;
      }
    }
  } catch (e) {
    errMsg = e.name === "AbortError" ? `<timeout after ${timeoutMs}ms>` : `<network: ${e.message ?? e}>`;
    body += errMsg;
  } finally {
    clearTimeout(timer);
  }
  const elapsedMs = Math.round(performance.now() - t0);
  const eventCount = (body.match(/^event: /gm) ?? []).length;
  const completed = body.includes("message_stop") || body.includes('"type":"message_stop"');
  // Stream error class: even with HTTP 200, the stream may be cut mid-flight.
  let errorClass = classifyError(status, body);
  if (errorClass === "ok" && !completed) errorClass = "stream_truncated";
  return {
    kind: "stream",
    status,
    elapsedMs,
    ttfbMs,
    ttftMs,
    streamMaxGapMs: Math.round(maxGapMs),
    streamEventCount: eventCount,
    streamCompleted: completed,
    outputTokens: outputTokens || null,
    errorClass,
    bodySnippet: body.slice(0, 240),
  };
}

// ---- Stats -----------------------------------------------------------------

function quantile(arr, q) {
  if (!arr.length) return null;
  const sorted = [...arr].sort((a, b) => a - b);
  const pos = (sorted.length - 1) * q;
  const lo = Math.floor(pos);
  const hi = Math.ceil(pos);
  if (lo === hi) return sorted[lo];
  return Math.round(sorted[lo] + (sorted[hi] - sorted[lo]) * (pos - lo));
}

function summarize(kind, results) {
  const all = results.filter((r) => r.kind === kind);
  const ok = all.filter((r) => r.errorClass === "ok");
  const errors = {};
  for (const r of all) {
    errors[r.errorClass] = (errors[r.errorClass] || 0) + 1;
  }
  const ttfb = ok.map((r) => r.ttfbMs).filter((x) => x != null);
  const elapsed = ok.map((r) => r.elapsedMs);
  const summary = {
    kind,
    n: all.length,
    success: ok.length,
    successRate: all.length ? +(ok.length / all.length).toFixed(4) : 0,
    errorClasses: errors,
    ttfbP50: quantile(ttfb, 0.5),
    ttfbP95: quantile(ttfb, 0.95),
    ttfbMax: ttfb.length ? Math.max(...ttfb) : null,
    elapsedP50: quantile(elapsed, 0.5),
    elapsedP95: quantile(elapsed, 0.95),
  };
  if (kind === "stream") {
    const ttft = ok.map((r) => r.ttftMs).filter((x) => x != null);
    const gaps = ok.map((r) => r.streamMaxGapMs).filter((x) => x != null);
    summary.ttftP50 = quantile(ttft, 0.5);
    summary.ttftP95 = quantile(ttft, 0.95);
    summary.maxGapP50 = quantile(gaps, 0.5);
    summary.maxGapP95 = quantile(gaps, 0.95);
    summary.streamCompletedRate = all.length
      ? +(all.filter((r) => r.streamCompleted).length / all.length).toFixed(4)
      : 0;
  }
  return summary;
}

function renderMd(meta, results, summaries) {
  const lines = [];
  lines.push(`# Stability Probe: ${meta.model}`);
  lines.push("");
  lines.push(`- **URL**: ${meta.url}`);
  lines.push(`- **Model**: ${meta.model}`);
  lines.push(`- **Iterations**: ${meta.iterations} × {nonstream, stream}`);
  lines.push(`- **Gap between iterations**: ${meta.gapMs}ms`);
  lines.push(`- **Per-request timeout**: ${meta.timeoutMs}ms`);
  lines.push(`- **Started**: ${meta.startedAt}`);
  lines.push(`- **Finished**: ${meta.finishedAt}`);
  lines.push(`- **Wall-clock**: ${meta.wallMs}ms`);
  lines.push("");
  lines.push("## Summary");
  lines.push("");
  for (const s of summaries) {
    lines.push(`### ${s.kind}`);
    lines.push("");
    lines.push(`- success rate: ${s.success}/${s.n} (${(s.successRate * 100).toFixed(1)}%)`);
    lines.push(`- error classes: ${JSON.stringify(s.errorClasses)}`);
    lines.push(`- TTFB (ms): p50=${s.ttfbP50} p95=${s.ttfbP95} max=${s.ttfbMax}`);
    lines.push(`- elapsed (ms): p50=${s.elapsedP50} p95=${s.elapsedP95}`);
    if (s.kind === "stream") {
      lines.push(`- TTFT (ms): p50=${s.ttftP50} p95=${s.ttftP95}`);
      lines.push(`- max-gap (ms): p50=${s.maxGapP50} p95=${s.maxGapP95}`);
      lines.push(`- stream completed rate: ${(s.streamCompletedRate * 100).toFixed(1)}%`);
    }
    lines.push("");
  }
  lines.push("## Per-iteration");
  lines.push("");
  lines.push("| # | kind | status | err | elapsed | ttfb | ttft | gap | tokens |");
  lines.push("|---|---|---|---|---|---|---|---|---|");
  for (const r of results) {
    lines.push(
      `| ${r.iter} | ${r.kind} | ${r.status} | ${r.errorClass} | ${r.elapsedMs} | ${r.ttfbMs ?? "-"} | ${r.ttftMs ?? "-"} | ${r.streamMaxGapMs ?? "-"} | ${r.outputTokens ?? "-"} |`,
    );
  }
  return lines.join("\n");
}

// ---- Main ------------------------------------------------------------------

async function main() {
  const { url, key, model, iterations, gapMs, timeoutMs, out } = parseArgs();
  const startedAt = new Date().toISOString();
  const t0 = performance.now();
  const results = [];

  for (let i = 1; i <= iterations; i++) {
    const ns = await nonstreamProbe(url, key, model, timeoutMs);
    ns.iter = i;
    results.push(ns);
    console.error(
      `[${model}] iter ${i}/${iterations} nonstream: ${ns.status} ${ns.errorClass} elapsed=${ns.elapsedMs}ms ttfb=${ns.ttfbMs}ms`,
    );
    if (gapMs > 0) await new Promise((r) => setTimeout(r, gapMs));

    const st = await streamProbe(url, key, model, timeoutMs);
    st.iter = i;
    results.push(st);
    console.error(
      `[${model}] iter ${i}/${iterations} stream:    ${st.status} ${st.errorClass} elapsed=${st.elapsedMs}ms ttfb=${st.ttfbMs}ms ttft=${st.ttftMs}ms gap=${st.streamMaxGapMs}ms events=${st.streamEventCount} done=${st.streamCompleted}`,
    );
    if (gapMs > 0) await new Promise((r) => setTimeout(r, gapMs));
  }

  const finishedAt = new Date().toISOString();
  const wallMs = Math.round(performance.now() - t0);
  const summaries = [summarize("nonstream", results), summarize("stream", results)];
  const meta = { url, model, iterations, gapMs, timeoutMs, startedAt, finishedAt, wallMs };

  const host = new URL(url).host.replace(/^api\./, "").replace(/^www\./, "").replace(/\./g, "-");
  const modelSlug = model.replace(/[^a-zA-Z0-9.-]/g, "-");
  const date = startedAt.slice(0, 10);
  const reportsDir = resolve(dirname(fileURLToPath(import.meta.url)), "stability-reports");
  if (!existsSync(reportsDir)) mkdirSync(reportsDir, { recursive: true });
  const base = out || resolve(reportsDir, `${host}-${modelSlug}-${date}`);
  writeFileSync(`${base}.json`, JSON.stringify({ meta, summaries, results }, null, 2));
  writeFileSync(`${base}.md`, renderMd(meta, results, summaries));
  console.error(`wrote ${base}.{json,md}`);
}

main().catch((e) => {
  console.error(e);
  exit(2);
});
