#!/usr/bin/env node
/**
 * Diagnostic: probe Anthropic prompt-cache minimum threshold for one or more
 * models. Sends system prompts of varying token counts with cache_control,
 * observes whether cache_creation_input_tokens > 0, finds the boundary.
 *
 * Use this when:
 *   - A new Claude model is released and the official docs threshold may be
 *     stale (e.g., Opus 4.7 docs say 4096 but actual is ~2048 as of 2026-05)
 *   - An endpoint reports 0/0 cache fields and you want to rule out
 *     "below threshold" vs "endpoint doesn't pass cache_control through"
 *
 * Usage:
 *   node tools/provider-eval/cache-threshold-probe.mjs <model1> [model2 ...]
 *
 * Example:
 *   node tools/provider-eval/cache-threshold-probe.mjs \
 *     claude-opus-4-7 claude-haiku-4-5-20251001
 *
 * Reads URL/KEY from .env (same as probe.mjs). Cost per model: ~$0.05-0.20.
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

const URL_ = env.URL;
const KEY = env.KEY;
const TARGETS = argv.slice(2);
if (TARGETS.length === 0) {
  console.error("usage: cache-threshold-probe.mjs <model1> [model2 ...]");
  process.exit(1);
}

function fillerBlocks(numBlocks, seed) {
  const sentence = `Test seed ${seed}: comprehensive analysis of distributed systems requires understanding consensus algorithms, fault tolerance mechanisms, network partitioning strategies, and eventual consistency models that govern modern cloud-native architectures and microservice deployment patterns across heterogeneous compute fabrics.`;
  // ~78 tokens per block (empirical from Sonnet 4-6 run)
  const lines = [];
  for (let i = 1; i <= numBlocks; i++) lines.push(`Block ${i}: ${sentence}`);
  return lines.join("\n");
}

async function probeAt(model, numBlocks) {
  const seed = `${model}-${numBlocks}-${Date.now()}-${Math.random()}`;
  const body = {
    model,
    max_tokens: 5,
    system: [
      {
        type: "text",
        text: `${fillerBlocks(numBlocks, seed)}\n\nAnswer briefly.`,
        cache_control: { type: "ephemeral" },
      },
    ],
    messages: [{ role: "user", content: "OK?" }],
  };
  const r = await fetch(`${URL_}/v1/messages`, {
    method: "POST",
    headers: {
      "x-api-key": KEY,
      "anthropic-version": "2023-06-01",
      "content-type": "application/json",
    },
    body: JSON.stringify(body),
  });
  let json;
  try {
    json = await r.json();
  } catch {
    return { error: `non-JSON status=${r.status}` };
  }
  if (!json.usage) return { error: json.error?.message ?? `no usage, status=${r.status}` };
  const u = json.usage;
  return {
    obs: u.input_tokens,
    create: u.cache_creation_input_tokens ?? 0,
    read: u.cache_read_input_tokens ?? 0,
    total: (u.input_tokens ?? 0) + (u.cache_creation_input_tokens ?? 0),
  };
}

// Default sweep covers 1024 → 5000 token range — enough resolution to bracket
// any of the documented thresholds (1024 / 2048 / 4096) within ~10%. Once you
// see the YES/NO boundary, edit this array for finer binary search.
const SWEEP = [13, 26, 39, 52, 56, 60, 65];

for (const model of TARGETS) {
  console.error(`\n=== ${model} ===`);
  console.error("blocks | obs   | cache_create | total | cached?");
  console.error("-------|-------|--------------|-------|--------");
  for (const n of SWEEP) {
    const r = await probeAt(model, n);
    if (r.error) {
      console.error(`${String(n).padEnd(6)} | ERROR: ${r.error}`);
      continue;
    }
    const cached = r.create > 0 ? "YES" : "NO ";
    console.error(
      `${String(n).padEnd(6)} | ${String(r.obs).padEnd(5)} | ${String(r.create).padEnd(12)} | ${String(r.total).padEnd(5)} | ${cached}`,
    );
    await new Promise((rs) => setTimeout(rs, 600));
  }
}
