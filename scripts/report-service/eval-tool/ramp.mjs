#!/usr/bin/env node
/**
 * RPS-ramp load test for Anthropic-compatible endpoint.
 *
 * Sends short PONG requests at increasing rates against multiple models in
 * round-robin. Each stage runs for --stage-sec seconds, then escalates to the
 * next rate. Stops early if a stage's error rate exceeds --abort-error-rate.
 *
 * Per-request: HTTP POST /v1/messages, max_tokens=10, "Reply PONG".
 *
 * Stdlib only; Node >= 18.
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
    models: env.MODELS ?? null, // comma-separated
    stages: "1,2,5,10,20,40",
    "stage-sec": "30",
    timeout: "30000",
    "abort-error-rate": "0.30", // abort when single stage errors > 30%
    out: null,
  };
  for (let i = 2; i < argv.length; i++) {
    const tok = argv[i];
    if (tok === "-h" || tok === "--help") {
      console.error(
        "Usage: ramp.mjs --url URL --key KEY --models 'm1,m2' [--stages 1,2,5,10] [--stage-sec 30] [--timeout 30000] [--abort-error-rate 0.30]",
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
  for (const r of ["url", "key", "models"]) {
    if (!a[r]) {
      console.error(`missing --${r}`);
      exit(1);
    }
  }
  return {
    url: a.url,
    key: a.key,
    models: a.models.split(",").map((s) => s.trim()).filter(Boolean),
    stages: a.stages.split(",").map((s) => Number(s.trim())).filter((n) => n > 0),
    stageSec: Math.max(5, Number(a["stage-sec"]) || 30),
    timeoutMs: Number(a.timeout) || 30000,
    abortErr: Number(a["abort-error-rate"]) || 0.3,
    out: a.out,
  };
}

function classify(status, bodyText) {
  if (status === 0) return bodyText.startsWith("<timeout") ? "timeout" : "network";
  if (status >= 200 && status < 300) return "ok";
  if (status === 524) return "cf_524";
  if (status === 429) return "429";
  if (status === 401 || status === 403) return "auth";
  if (status >= 500) return "5xx";
  if (status >= 400) return "4xx";
  return "unknown";
}

async function fire(url, key, model, timeoutMs) {
  const t0 = performance.now();
  const ctrl = new AbortController();
  const timer = setTimeout(() => ctrl.abort(), timeoutMs);
  let status = 0;
  let body = "";
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
        messages: [{ role: "user", content: "Reply PONG" }],
      }),
      signal: ctrl.signal,
    });
    status = r.status;
    body = await r.text();
  } catch (e) {
    body = e.name === "AbortError" ? `<timeout after ${timeoutMs}ms>` : `<network: ${e.message ?? e}>`;
  } finally {
    clearTimeout(timer);
  }
  const elapsedMs = Math.round(performance.now() - t0);
  const errorClass = classify(status, body);
  return { model, status, elapsedMs, errorClass, bodySnippet: body.slice(0, 200) };
}

function quantile(arr, q) {
  if (!arr.length) return null;
  const s = [...arr].sort((a, b) => a - b);
  const pos = (s.length - 1) * q;
  const lo = Math.floor(pos);
  const hi = Math.ceil(pos);
  return lo === hi ? s[lo] : Math.round(s[lo] + (s[hi] - s[lo]) * (pos - lo));
}

function summarizeStage(stage) {
  const all = stage.results;
  const ok = all.filter((r) => r.errorClass === "ok");
  const errs = {};
  for (const r of all) errs[r.errorClass] = (errs[r.errorClass] || 0) + 1;
  const elapsed = ok.map((r) => r.elapsedMs);
  const allElapsed = all.map((r) => r.elapsedMs);
  // per-model breakdown
  const byModel = {};
  for (const r of all) {
    const m = r.model;
    byModel[m] ||= { n: 0, ok: 0, errs: {} };
    byModel[m].n++;
    if (r.errorClass === "ok") byModel[m].ok++;
    byModel[m].errs[r.errorClass] = (byModel[m].errs[r.errorClass] || 0) + 1;
  }
  return {
    targetRps: stage.targetRps,
    durSec: stage.durSec,
    fired: stage.fired,
    completed: all.length,
    inflightAtEnd: stage.fired - all.length, // requests that didn't return before stage ended
    success: ok.length,
    successRate: all.length ? +(ok.length / all.length).toFixed(4) : 0,
    actualRps: +(stage.fired / stage.durSec).toFixed(2),
    actualCompletedRps: +(all.length / stage.durSec).toFixed(2),
    errors: errs,
    elapsedOk: {
      p50: quantile(elapsed, 0.5),
      p95: quantile(elapsed, 0.95),
      p99: quantile(elapsed, 0.99),
      max: elapsed.length ? Math.max(...elapsed) : null,
    },
    elapsedAll: {
      p50: quantile(allElapsed, 0.5),
      p95: quantile(allElapsed, 0.95),
      p99: quantile(allElapsed, 0.99),
    },
    byModel,
  };
}

async function runStage(url, key, models, targetRps, durSec, timeoutMs) {
  const intervalMs = 1000 / targetRps;
  const results = [];
  const inflight = [];
  let fired = 0;
  const t0 = performance.now();
  const tEnd = t0 + durSec * 1000;

  let modelIdx = 0;
  let nextFireAt = t0;

  while (performance.now() < tEnd) {
    const now = performance.now();
    if (now >= nextFireAt) {
      const model = models[modelIdx % models.length];
      modelIdx++;
      fired++;
      const p = fire(url, key, model, timeoutMs)
        .then((r) => {
          results.push(r);
          const idx = inflight.indexOf(p);
          if (idx >= 0) inflight.splice(idx, 1);
        });
      inflight.push(p);
      nextFireAt += intervalMs;
      // catch up if we slipped (clamp burst)
      if (nextFireAt < now - 200) nextFireAt = now;
    } else {
      const sleep = Math.min(nextFireAt - now, 50);
      await new Promise((r) => setTimeout(r, sleep));
    }
  }
  // Drain: wait up to timeout for inflight to settle so stage stats are accurate.
  const drainStart = performance.now();
  while (inflight.length > 0 && performance.now() - drainStart < timeoutMs + 5000) {
    await Promise.race([
      Promise.all([...inflight]),
      new Promise((r) => setTimeout(r, 500)),
    ]);
  }
  const actualDur = (performance.now() - t0) / 1000;
  return { targetRps, durSec, fired, results, actualDur };
}

function renderMd(meta, stages, summaries) {
  const lines = [];
  lines.push(`# Load Test (RPS ramp): ${meta.url}`);
  lines.push("");
  lines.push(`- **Models** (round-robin): ${meta.models.join(", ")}`);
  lines.push(`- **Stages (rps)**: ${meta.stages.join(", ")}`);
  lines.push(`- **Stage duration**: ${meta.stageSec}s`);
  lines.push(`- **Per-request timeout**: ${meta.timeoutMs}ms`);
  lines.push(`- **Abort threshold**: ${(meta.abortErr * 100).toFixed(0)}% errors`);
  lines.push(`- **Started**: ${meta.startedAt}`);
  lines.push(`- **Finished**: ${meta.finishedAt}`);
  lines.push("");
  lines.push("## Summary");
  lines.push("");
  lines.push("| target rps | fired | completed | inflight@end | succ | succ rate | p50 (ok) | p95 (ok) | p99 (ok) | errors |");
  lines.push("|---|---|---|---|---|---|---|---|---|---|");
  for (const s of summaries) {
    lines.push(
      `| ${s.targetRps} | ${s.fired} | ${s.completed} | ${s.inflightAtEnd} | ${s.success} | ${(s.successRate * 100).toFixed(1)}% | ${s.elapsedOk.p50 ?? "-"} | ${s.elapsedOk.p95 ?? "-"} | ${s.elapsedOk.p99 ?? "-"} | ${JSON.stringify(s.errors)} |`,
    );
  }
  lines.push("");
  lines.push("## Per-model breakdown (per stage)");
  for (const s of summaries) {
    lines.push("");
    lines.push(`### Stage ${s.targetRps} rps`);
    lines.push("");
    lines.push("| model | n | ok | succ rate | errors |");
    lines.push("|---|---|---|---|---|");
    for (const [m, v] of Object.entries(s.byModel)) {
      lines.push(`| ${m} | ${v.n} | ${v.ok} | ${((v.ok / v.n) * 100).toFixed(1)}% | ${JSON.stringify(v.errs)} |`);
    }
  }
  return lines.join("\n");
}

async function main() {
  const { url, key, models, stages, stageSec, timeoutMs, abortErr, out } = parseArgs();
  const startedAt = new Date().toISOString();
  const summaries = [];
  const allStages = [];

  for (const rps of stages) {
    console.error(`\n=== STAGE ${rps} rps × ${stageSec}s × ${models.length} model(s) ===`);
    const stage = await runStage(url, key, models, rps, stageSec, timeoutMs);
    const sum = summarizeStage(stage);
    summaries.push(sum);
    allStages.push({ ...stage, summary: sum });
    console.error(
      `[stage ${rps} rps] fired=${sum.fired} completed=${sum.completed} succ=${sum.success}/${sum.completed} (${(sum.successRate * 100).toFixed(1)}%) p50=${sum.elapsedOk.p50}ms p95=${sum.elapsedOk.p95}ms errors=${JSON.stringify(sum.errors)}`,
    );
    const errRate = 1 - sum.successRate;
    if (errRate >= abortErr && sum.completed > 5) {
      console.error(
        `[ABORT] stage ${rps} rps error rate ${(errRate * 100).toFixed(1)}% >= ${(abortErr * 100).toFixed(0)}%`,
      );
      break;
    }
  }

  const finishedAt = new Date().toISOString();
  const meta = { url, models, stages, stageSec, timeoutMs, abortErr, startedAt, finishedAt };
  const host = new URL(url).host.replace(/^api\./, "").replace(/^www\./, "").replace(/\./g, "-");
  const date = startedAt.slice(0, 10);
  const reportsDir = resolve(dirname(fileURLToPath(import.meta.url)), "stability-reports");
  if (!existsSync(reportsDir)) mkdirSync(reportsDir, { recursive: true });
  const base = out || resolve(reportsDir, `${host}-ramp-${date}`);
  writeFileSync(`${base}.json`, JSON.stringify({ meta, summaries, stages: allStages }, null, 2));
  writeFileSync(`${base}.md`, renderMd(meta, allStages, summaries));
  console.error(`\nwrote ${base}.{json,md}`);
}

main().catch((e) => {
  console.error(e);
  exit(2);
});
