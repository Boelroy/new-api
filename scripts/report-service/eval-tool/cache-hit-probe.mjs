#!/usr/bin/env node
/**
 * Diagnostic: verify prompt-cache HIT (read) behavior end-to-end for one model.
 *
 * Unlike cache-threshold-probe.mjs (which only checks cache CREATION), this
 * sends the SAME large cache_control'd system prompt twice:
 *   - Request #1 (cold): expect cache_creation_input_tokens > 0, read == 0
 *   - Request #2 (warm): expect cache_read_input_tokens > 0, creation == 0
 *
 * A working cache shows the bulk of the system prompt moving from "creation"
 * on call #1 to "read" on call #2. If call #2 still shows creation > 0 and
 * read == 0, the endpoint is NOT serving cache hits (e.g. it strips
 * cache_control, or routes the two calls to different upstream nodes).
 *
 * Usage:
 *   node tools/provider-eval/cache-hit-probe.mjs [model]
 * Defaults to MODEL from .env. Reads URL/KEY/MODEL from .env (same as probe.mjs).
 */
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { readFileSync } from "node:fs";
import { argv, env } from "node:process";

const here = dirname(fileURLToPath(import.meta.url));
const envPath = resolve(here, ".env");
for (const raw of readFileSync(envPath, "utf8").split("\n")) {
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

const URL_ = env.URL; // already includes /v1 for this endpoint
const KEY = env.KEY;
const MODEL = argv[2] || env.MODEL;
if (!URL_ || !KEY || !MODEL) {
  console.error("missing URL/KEY/MODEL (set in .env or pass model arg)");
  process.exit(1);
}

// This gateway (maasapi.anispark.ai) uses metadata.user_id for STICKY upstream
// routing. Without it, calls fan out across cold nodes and hit rate is ~30%.
// With a stable user_id, requests pin to one warmed node → ~90% hit rate.
// Pass a different id as argv[3] to test isolation; "" disables it.
const USER_ID = argv[3] !== undefined ? argv[3] : "cache-hit-probe-uid-0001";

// ~3000 tokens of stable filler — comfortably above any documented cache
// minimum (1024/2048/4096). Stable across both calls so the cache key matches.
function bigSystem() {
  const sentence =
    "Comprehensive analysis of distributed systems requires understanding consensus algorithms, fault tolerance mechanisms, network partitioning strategies, and eventual consistency models that govern modern cloud-native architectures and microservice deployment patterns across heterogeneous compute fabrics.";
  const lines = [];
  for (let i = 1; i <= 120; i++) lines.push(`Block ${i}: ${sentence}`);
  return lines.join("\n") + "\n\nAnswer briefly.";
}

const SYSTEM_TEXT = bigSystem();

async function call(label) {
  const body = {
    model: MODEL,
    max_tokens: 5,
    system: [
      { type: "text", text: SYSTEM_TEXT, cache_control: { type: "ephemeral" } },
    ],
    messages: [{ role: "user", content: "Reply with OK." }],
  };
  if (USER_ID) body.metadata = { user_id: USER_ID };
  const r = await fetch(`${URL_}/messages`, {
    method: "POST",
    headers: {
      "x-api-key": KEY,
      "anthropic-version": "2023-06-01",
      "content-type": "application/json",
    },
    body: JSON.stringify(body),
  });
  const json = await r.json().catch(() => null);
  if (!json || !json.usage) {
    console.error(`${label}: ERROR status=${r.status} ${JSON.stringify(json)?.slice(0, 200)}`);
    return null;
  }
  const u = json.usage;
  return {
    input: u.input_tokens ?? 0,
    create: u.cache_creation_input_tokens ?? 0,
    read: u.cache_read_input_tokens ?? 0,
  };
}

console.error(`Model: ${MODEL}`);
console.error(`System prompt: ${SYSTEM_TEXT.length} chars (~${Math.round(SYSTEM_TEXT.length / 4)} tokens est.)`);
console.error(`metadata.user_id: ${USER_ID || "(none — sticky routing disabled)"}\n`);
console.error("call   | input | cache_create | cache_read | verdict");
console.error("-------|-------|--------------|------------|--------");

const r1 = await call("cold");
if (r1)
  console.error(
    `#1 cold| ${String(r1.input).padEnd(5)} | ${String(r1.create).padEnd(12)} | ${String(r1.read).padEnd(10)} | ${r1.create > 0 ? "cache WRITTEN" : "no write"}`,
  );

// brief pause so the write is committed upstream before the warm read
await new Promise((rs) => setTimeout(rs, 1500));

const r2 = await call("warm");
if (r2)
  console.error(
    `#2 warm| ${String(r2.input).padEnd(5)} | ${String(r2.create).padEnd(12)} | ${String(r2.read).padEnd(10)} | ${r2.read > 0 ? "cache HIT ✓" : "MISS ✗"}`,
  );

console.error("");
if (r1 && r2) {
  if (r2.read > 0) {
    console.error(`RESULT: ✅ cache hit works — ${r2.read} tokens served from cache on warm call.`);
  } else if (r1.create > 0) {
    console.error("RESULT: ⚠️  cache is WRITTEN but never READ — warm call shows read=0 (no hit).");
  } else {
    console.error("RESULT: ❌ no caching at all — neither create nor read observed.");
  }
}
