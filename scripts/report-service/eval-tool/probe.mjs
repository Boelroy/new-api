#!/usr/bin/env node
/**
 * Pure trace collector for LLM provider evaluation (performance / IQ / features).
 *
 * Sends ~57 probes (covering 31 logical steps) against an Anthropic Messages
 * API endpoint, captures full HTTP traces with precise timing (TTFB, TTFT,
 * throughput), and writes a single Markdown bundle.  Performs NO scoring —
 * analysis is done by reading this bundle alongside PIPELINE.md (typically
 * by an LLM such as Claude Code).
 *
 * Includes per-run dynamic randomization for memorization-resistant IQ probes
 * (Q4 math, Q27 sort, Q33 filter, Q35 boolean, Q38 unit conversion, Q39
 * probability) and a per-run salt prepended to every user-role prompt so
 * upstream gateway prompt-caches don't dedup across evaluation runs.
 *
 * Stdlib only.  Requires Node >= 18 (native fetch).
 */
import { existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { argv, env, exit, stdout } from "node:process";

// ---------------------------------------------------------------------------
// Config
// ---------------------------------------------------------------------------

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
    if (
      (v.startsWith('"') && v.endsWith('"')) ||
      (v.startsWith("'") && v.endsWith("'"))
    )
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
    out: null,
    timeout: "60000",
    repeat: "1",
    runid: env.RUN_ID ?? null,
  };
  for (let i = 2; i < argv.length; i++) {
    const tok = argv[i];
    if (tok === "-h" || tok === "--help") help(0);
    if (!tok.startsWith("--")) {
      console.error(`unknown positional: ${tok}`);
      help(1);
    }
    const k = tok.slice(2);
    if (!(k in a)) {
      console.error(`unknown flag: --${k}`);
      help(1);
    }
    a[k] = argv[++i];
  }
  for (const r of ["url", "key", "model"]) {
    if (!a[r]) {
      console.error(
        `missing required --${r} (or ${r.toUpperCase()} in .env)`,
      );
      help(1);
    }
  }
  const repeat = Math.max(1, Number(a.repeat) || 1);
  const runId = a.runid ?? Math.random().toString(36).slice(2, 10);
  return {
    ...a,
    timeout: Number(a.timeout) || 60000,
    repeat,
    runId,
  };
}

// ─── Shared per-run randomization (mirrors backend/src/services/probeLibrary/) ───

/**
 * Prepend a single sentence that varies the input bytes per run.
 * Format is plain English so input-shape safety classifiers on adaptive-
 * thinking models don't pattern-match it as machine-generated markup.
 * See backend/src/services/probeLibrary/runSalt.ts for the historical rationale.
 */
function withRunSalt(content, runId) {
  const tag = runId ? runId.slice(0, 8) : "no-run-id";
  return `(Evaluation pass ${tag}.)\n\n${content}`;
}

/**
 * Deterministic, runId-seeded LCG. Reproducible: same runId → same sequence.
 * Quality is fine for "vary benchmark inputs", NOT for cryptography.
 */
function createSeededRng(runId) {
  let seed = 0;
  const src = runId ?? "unknown-run";
  for (let i = 0; i < src.length; i++) {
    seed = (seed * 31 + src.charCodeAt(i)) >>> 0;
  }
  if (seed === 0) seed = 1;
  return () => {
    seed = (seed * 1664525 + 1013904223) >>> 0;
    return seed / 0x1_0000_0000;
  };
}

/** Pick a random integer in [lo, hi] inclusive. */
function randInt(rng, lo, hi) {
  return lo + Math.floor(rng() * (hi - lo + 1));
}

/** Pick one element from an array. */
function pickOne(rng, arr) {
  return arr[Math.floor(rng() * arr.length)];
}

/**
 * Compute the greatest common divisor of two non-negative integers. Used by
 * the probability probe to reduce fractions to lowest terms.
 */
function gcd(a, b) {
  a = Math.abs(a);
  b = Math.abs(b);
  while (b !== 0) {
    const t = b;
    b = a % b;
    a = t;
  }
  return a || 1;
}

// ─── Dynamic IQ probe generators ─────────────────────────────────────────
// Each generator takes a seeded PRNG and returns { content, expected }:
//   content  — prompt text the model sees
//   expected — string the judge compares against (whitespace-tolerant)

/** Q4: store math. Random fruit prices + quantities. Total = ap*ac + op*oc. */
function generateQ4Math(rng) {
  const applePrice = randInt(rng, 2, 6);
  const orangePrice = randInt(rng, 2, 6);
  const appleCount = randInt(rng, 3, 12);
  const orangeCount = randInt(rng, 2, 10);
  const total = applePrice * appleCount + orangePrice * orangeCount;
  return {
    content: `A store sells apples at $${applePrice} each and oranges at $${orangePrice} each. I buy ${appleCount} apples and ${orangeCount} oranges. How much do I spend in total? Reply with just the dollar amount (a single number).`,
    expected: String(total),
  };
}

/** Q27: sort 8-10 distinct integers in [1, 99]. */
function generateQ27Sort(rng) {
  const n = randInt(rng, 8, 10);
  const used = new Set();
  while (used.size < n) used.add(randInt(rng, 1, 99));
  const nums = [...used];
  const sorted = [...nums].sort((a, b) => a - b);
  return {
    content: `Sort these numbers in ascending order and output ONLY the sorted list as comma-separated values, no other text: ${nums.join(", ")}`,
    expected: sorted.join(","),
  };
}

/** Q33: BBH object_counting — random fruit/non-fruit counts + random exclusions. */
function generateQ33Filter(rng) {
  const fruitsAll = ["apple", "cherry", "pear", "banana", "plum", "grape", "mango", "peach"];
  const nonFruits = ["spoon", "fork", "napkin", "plate", "cup", "knife"];
  const shuffled = [...fruitsAll].sort(() => rng() - 0.5);
  const keep = shuffled.slice(0, 2);
  const exclude = shuffled.slice(2, 4);
  const decoys = [...nonFruits].sort(() => rng() - 0.5).slice(0, 3);
  const items = [];
  for (const f of keep) items.push({ name: f, count: randInt(rng, 1, 9), counted: true });
  for (const f of exclude) items.push({ name: f, count: randInt(rng, 1, 9), counted: false });
  for (const d of decoys) items.push({ name: d, count: randInt(rng, 1, 9), counted: false });
  items.sort(() => rng() - 0.5);
  const lines = items.map((it) => `${it.count} ${it.name}${it.count === 1 ? "" : "s"}`).join(", ");
  const expected = items.filter((it) => it.counted).reduce((s, it) => s + it.count, 0);
  const excludeLabel = exclude.map((f) => `${f}s`).join(" and ");
  return {
    content: `I own: ${lines}. How many pieces of fruit do I have, excluding ${excludeLabel}? Reply with just a number.`,
    expected: String(expected),
  };
}

/** Q35: BBH boolean — random 4-variable expression. */
function generateQ35Boolean(rng) {
  const tf = () => (rng() < 0.5 ? "True" : "False");
  const op = () => (rng() < 0.5 ? "and" : "or");
  const A = tf();
  const B = tf();
  const C = tf();
  const D = tf();
  const op1 = op();
  const op2 = op();
  const negD = rng() < 0.5;
  const expr = `not (${A} ${op1} ${B}) ${op2} (${C} ${op1} ${negD ? "not " : ""}${D})`;
  const tv = (s) => s === "True";
  const opEval = (x, o, y) => (o === "and" ? x && y : x || y);
  const left = !opEval(tv(A), op1, tv(B));
  const dEff = negD ? !tv(D) : tv(D);
  const right = opEval(tv(C), op1, dEff);
  const result = opEval(left, op2, right);
  return {
    content: `Evaluate this expression and reply with only "True" or "False":\n${expr}`,
    expected: result ? "True" : "False",
  };
}

/** Q38: unit conversion mg/s × hours → kg. */
function generateQ38UnitConv(rng) {
  const rateMg = randInt(rng, 50, 500);
  const hours = randInt(rng, 2, 12);
  const seconds = hours * 3600;
  const totalKg = (rateMg * seconds) / 1_000_000;
  const expected = (Math.round(totalKg * 10000) / 10000).toString();
  return {
    content: `A device consumes ${rateMg} milligrams of fuel per second. How many kilograms does it consume over ${hours} hours? Reply with just the number (no units, no commas).`,
    expected,
  };
}

/** Q39: probability of drawing 2 reds without replacement, in lowest terms. */
function generateQ39Probability(rng) {
  const red = randInt(rng, 2, 6);
  const blue = randInt(rng, 2, 6);
  const green = randInt(rng, 2, 6);
  const total = red + blue + green;
  const num = red * (red - 1);
  const den = total * (total - 1);
  const g = gcd(num, den);
  return {
    content: `A bag has ${red} red, ${blue} blue, and ${green} green marbles. You draw 2 marbles without replacement. What is the probability that both are red? Reply with just a fraction in lowest terms, like "a/b".`,
    expected: `${num / g}/${den / g}`,
  };
}
// pickOne is exported for parity with the backend module; unused inside probe.mjs itself.
void pickOne;

function help(code) {
  console.error(`Usage: probe.mjs [--url URL] [--key KEY] [--model MODEL] [--out PATH] [--timeout MS] [--runid ID]

Send ~57 evaluation probes to a Claude/Anthropic Messages API endpoint and
write a Markdown trace bundle.  No scoring logic — analysis is done by
reading the bundle alongside PIPELINE.md (e.g. feed both to Claude Code).

Inputs (each can come from CLI flag OR same-name env var, e.g. URL=...):
  --url       Endpoint base URL (e.g. https://api.example.com)
  --key       API key (sent as both x-api-key and Authorization: Bearer)
  --model     Model id (run --url + GET /v1/models first if unsure)

Credential file:
  Place a \`.env\` next to this script with URL=, KEY=, MODEL= lines.
  The script auto-loads it.  \`.env\` is gitignored; \`.env.example\` shows
  the format.

Optional:
  --out       Output path. If omitted, auto-derived as
              \`reports/<host>-<model>-<YYYY-MM-DD>-trace.md\`
              (next to this script). Pass \`--out -\` to write to stdout.
  --timeout   Per-probe timeout in ms (default: 60000)
  --repeat N  Repeat each IQ probe N times (pass@N, default 1). Cost scales
              ~linearly with N for IQ steps; non-IQ steps (models catalog,
              streaming, long output, caching, 1M context, errors) run once
              regardless. Use --repeat 3 to dampen stochastic noise (LLM
              eval best practice; matches IFEval/Arena-Hard methodology).
  --runid ID  Explicit run identifier (also readable from RUN_ID env var).
              Seeds the dynamic-probe PRNG AND the per-run prompt salt so
              reruns with the same id produce identical inputs. If omitted,
              a random 8-char id is generated per invocation.

Probes sent (~57 requests, ~180-360s wall-clock):
  Step 0  :  GET /v1/models
  Step 1  :  Plain instruction compliance (non-streaming)
  Step 2  :  Streaming count 1-20 (TTFT + throughput)
  Step 3  :  Tool use (forced via tool_choice)
  Step 3f :  Tool use (auto tool_choice) — fallback if forced was rejected
  Step 4  :  Math reasoning — dynamic per-run inputs (verifiable answer)
  Step 4b :  Instruction resistance (STOP trap)
  Step 4c-4D:  IQ test battery (Q12-Q28 + Q31-Q41, 28 tests). Q27 sort, Q33
              filter, Q35 boolean, Q38 unit conv, Q39 probability are DYNAMIC
              (fresh inputs per run, expected value computed per run).
  Step 5  :  JSON structured output (free-form)
  Step 6  :  Long output (sustained throughput)
  Step 7  :  Multi-turn context recall
  Step 8  :  System prompt directive compliance
  Step 9a :  Caching cold (first request)
  Step 9b :  Caching warm (identical request)
  Step 10 :  Temperature=0 reproducibility (x2)
  Step 11 :  Structured output (json_schema)
  Step 12 :  1M context test (needle-in-haystack, ~1M input tokens)
  Step 13 :  Vision multimodal (image input — uses test-image.png)
  Step 14 :  Extended thinking (adaptive or legacy schema)
  Step 14f:  Extended thinking (fallback schema) — if primary was rejected
  Step 15a:  Error recovery — bad request (role='alien')
  Step 15b:  Error recovery — valid request after error
  Step 16 :  Tool search tool (F15 — beta: tool-search-tool-2025-10-19)
  Step 16f:  Tool search fallback header (advanced-tool-use-2025-11-20)
  Step 17 :  Context-management edits / clear_tool_uses_20250919 (F16)
  Step 18 :  Server-side compaction / compact_20260112 (F17)
  Step 19 :  Computer use beta header + computer_20251124 tool (F18)
  Step 20 :  Fine-grained tool streaming / eager_input_streaming (F19)
  Step 21 :  Multi-beta header coexistence (F20)
`);
  exit(code);
}

// ---------------------------------------------------------------------------
// HTTP helpers
// ---------------------------------------------------------------------------

const TOOL = {
  name: "get_weather",
  description: "Get the current weather for a city",
  input_schema: {
    type: "object",
    properties: { city: { type: "string" } },
    required: ["city"],
  },
};

// Constant account-sticky id: makes multi-account new-api gateways pin every
// request to one upstream org so prompt-cache reads actually hit (F5).
const STICKY_USER_ID = "provider-eval-sticky-001";
function withMeta(body) {
  if (!body || typeof body !== "object") return body;
  return { ...body, metadata: { user_id: STICKY_USER_ID, ...(body.metadata || {}) } };
}

// Global run id + salt callable. Set once in main(); referenced by probe/streamProbe.
let CURRENT_RUN_ID = null;

/**
 * Return a shallow-cloned body with the per-run salt prepended to the FIRST
 * user-role message's first text content. Non-user roles are skipped so the
 * bad-request probe (role='alien') still triggers the intended 4xx.
 *
 * Anthropic message content shape handled:
 *   - string             → prepended directly
 *   - array of blocks    → first block with type 'text' has its .text prepended
 * Non-text blocks (image, tool_use, tool_result, thinking) are left alone.
 */
function applySaltToBody(body, runId) {
  if (!body || typeof body !== "object" || !Array.isArray(body.messages) || !runId) {
    return body;
  }
  const newMessages = body.messages.slice();
  for (let i = 0; i < newMessages.length; i++) {
    const msg = newMessages[i];
    if (!msg || msg.role !== "user") continue;
    if (typeof msg.content === "string") {
      newMessages[i] = { ...msg, content: withRunSalt(msg.content, runId) };
    } else if (Array.isArray(msg.content)) {
      const newContent = msg.content.slice();
      for (let j = 0; j < newContent.length; j++) {
        const block = newContent[j];
        if (block && block.type === "text" && typeof block.text === "string") {
          newContent[j] = { ...block, text: withRunSalt(block.text, runId) };
          break;
        }
      }
      newMessages[i] = { ...msg, content: newContent };
    }
    break; // only salt the first user-role message
  }
  return { ...body, messages: newMessages };
}

// ─── F13 (extended thinking) per-model schema dispatch ───────────────────
// Mirrors backend/src/services/evalProbes/runner.ts:pickThinkingSchema.
// Anthropic split the thinking-config schema across model families:
//   - opus-4-7 / 4-8 / fable-5 / mythos-5: adaptive-only. Manual
//     enabled+budget_tokens returns HTTP 400.
//   - opus-4-6 / sonnet-4-6: both work; adaptive preferred (legacy deprecated).
//   - opus/sonnet/haiku-4-5 and earlier: legacy enabled+budget_tokens only.
//   - Unknown / proxy-prefixed ids: legacy first, 14-fallback upgrades.
function pickThinkingSchema(model) {
  const last = model.toLowerCase().split("/").pop() ?? "";
  const id = last.replace(/-\d{8}$/, "").replace(/-\d{4}-\d{2}-\d{2}$/, "");
  if (
    id === "claude-opus-4-7" ||
    id === "claude-opus-4-8" ||
    id === "claude-fable-5" ||
    id === "claude-mythos-5" ||
    id.startsWith("claude-opus-4-7-") ||
    id.startsWith("claude-opus-4-8-") ||
    id.startsWith("claude-mythos-preview")
  ) {
    return "adaptive";
  }
  if (id === "claude-opus-4-6" || id === "claude-sonnet-4-6") {
    return "adaptive";
  }
  return "legacy";
}

function adaptiveThinkingPayload() {
  return {
    thinking: { type: "adaptive", display: "summarized" },
    output_config: { effort: "high" },
  };
}

function legacyThinkingPayload() {
  return { thinking: { type: "enabled", budget_tokens: 1024 } };
}

async function probe(url, key, path, body, label, intent, timeoutMs, opts2 = {}) {
  const { skipSalt = false, extraHeaders = null } = opts2;
  const t0 = performance.now();
  let ttfbMs = null;
  let status = 0;
  let headers = {};
  let bodyText = "";
  const ctrl = new AbortController();
  const timer = setTimeout(() => ctrl.abort(), timeoutMs);
  try {
    const saltedBody = skipSalt ? body : applySaltToBody(body, CURRENT_RUN_ID);
    const postHeaders = {
      "x-api-key": key,
      "anthropic-version": "2023-06-01",
      "content-type": "application/json",
    };
    if (extraHeaders) Object.assign(postHeaders, extraHeaders);
    const opts =
      body == null
        ? {
            method: "GET",
            headers: { Authorization: `Bearer ${key}` },
            signal: ctrl.signal,
          }
        : {
            method: "POST",
            headers: postHeaders,
            body: JSON.stringify(withMeta(saltedBody)),
            signal: ctrl.signal,
          };
    const r = await fetch(`${url}${path}`, opts);
    ttfbMs = Math.round(performance.now() - t0);
    status = r.status;
    headers = Object.fromEntries(r.headers);
    bodyText = await r.text();
  } catch (e) {
    bodyText =
      e.name === "AbortError"
        ? `<timeout after ${timeoutMs}ms>`
        : `<network error: ${e.message ?? e}>`;
  } finally {
    clearTimeout(timer);
  }
  return {
    label,
    intent,
    status,
    headers,
    body: bodyText,
    elapsedMs: Math.round(performance.now() - t0),
    ttfbMs,
  };
}

async function streamProbe(url, key, body, label, intent, timeoutMs, opts2 = {}) {
  const { skipSalt = false, extraHeaders = null } = opts2;
  const t0 = performance.now();
  let ttfbMs = null;
  let ttftMs = null;
  let status = 0;
  let headers = {};
  let chunks = [];
  let maxGapMs = 0;
  let lastByteAt = performance.now();
  let outputTokens = 0;
  const ctrl = new AbortController();
  const timer = setTimeout(() => ctrl.abort(), timeoutMs);
  try {
    const saltedBody = skipSalt ? body : applySaltToBody(body, CURRENT_RUN_ID);
    const postHeaders = {
      "x-api-key": key,
      "anthropic-version": "2023-06-01",
      "content-type": "application/json",
    };
    if (extraHeaders) Object.assign(postHeaders, extraHeaders);
    const r = await fetch(`${url}/v1/messages`, {
      method: "POST",
      headers: postHeaders,
      body: JSON.stringify({ ...withMeta(saltedBody), stream: true }),
      signal: ctrl.signal,
    });
    ttfbMs = Math.round(performance.now() - t0);
    status = r.status;
    headers = Object.fromEntries(r.headers);
    if (r.body) {
      const reader = r.body.getReader();
      const decoder = new TextDecoder();
      let chunkIdx = 0;
      while (true) {
        const { value, done } = await reader.read();
        if (done) break;
        const now = performance.now();
        const text = decoder.decode(value, { stream: true });
        chunks.push(text);
        if (chunkIdx > 0) {
          const gap = now - lastByteAt;
          if (gap > maxGapMs) maxGapMs = gap;
        }
        lastByteAt = now;
        // TTFT: first chunk containing content_block_delta
        if (ttftMs === null && text.includes("content_block_delta")) {
          ttftMs = Math.round(now - t0);
        }
        // output_tokens from message_delta
        const m = text.match(/"output_tokens"\s*:\s*(\d+)/);
        if (m) outputTokens = parseInt(m[1], 10);
        chunkIdx++;
      }
    }
  } catch (e) {
    chunks.push(
      e.name === "AbortError"
        ? `<timeout after ${timeoutMs}ms>`
        : `<network error: ${e.message ?? e}>`,
    );
  } finally {
    clearTimeout(timer);
  }
  const bodyText = chunks.join("");
  const elapsedMs = Math.round(performance.now() - t0);
  const genMs = elapsedMs - (ttfbMs ?? 0);
  const throughput =
    outputTokens > 0 && genMs > 0
      ? Math.round((outputTokens / genMs) * 1000)
      : null;
  return {
    label,
    intent,
    status,
    headers,
    body: bodyText,
    elapsedMs,
    ttfbMs,
    ttftMs,
    outputTokens,
    throughputTokPerSec: throughput,
    streamMaxGapMs: Math.round(maxGapMs),
    streamEventCount: (bodyText.match(/^event: /gm) ?? []).length,
  };
}

// ---------------------------------------------------------------------------
// Formatting
// ---------------------------------------------------------------------------

function fmtHeaders(h) {
  return Object.entries(h)
    .map(([k, v]) => `${k}: ${v}`)
    .join("\n");
}

function fmtBody(t) {
  const ct = (t.headers["content-type"] || "").toLowerCase();
  if (ct.includes("application/json") && t.body) {
    try {
      return JSON.stringify(JSON.parse(t.body), null, 2);
    } catch {
      /* fall through to raw */
    }
  }
  return t.body;
}

function perfLine(t) {
  const parts = [];
  if (t.ttfbMs != null) parts.push(`TTFB=${t.ttfbMs}ms`);
  if (t.ttftMs != null) parts.push(`TTFT=${t.ttftMs}ms`);
  if (t.throughputTokPerSec != null)
    parts.push(`throughput=${t.throughputTokPerSec}tok/s`);
  if (t.outputTokens != null) parts.push(`output_tokens=${t.outputTokens}`);
  if (t.streamMaxGapMs != null) parts.push(`max-gap=${t.streamMaxGapMs}ms`);
  if (t.streamEventCount != null)
    parts.push(`events=${t.streamEventCount}`);
  return parts.length ? ` | ${parts.join(" ")}` : "";
}

function renderTrace(t) {
  return `## ${t.label}

**Intent**: ${t.intent}

**HTTP**: ${t.status} (${t.elapsedMs}ms)${perfLine(t)}

### Headers

\`\`\`
${fmtHeaders(t.headers) || "<none>"}
\`\`\`

### Body

\`\`\`
${fmtBody(t) || "<empty>"}
\`\`\`
`;
}

// ---------------------------------------------------------------------------
// Probes
// ---------------------------------------------------------------------------

async function main() {
  const { url, key, model, out, timeout, repeat, runId } = parseArgs();
  CURRENT_RUN_ID = runId;
  const rng = createSeededRng(runId);
  const startedAt = new Date().toISOString();
  const traces = [];

  // Helper for single-prompt IQ probes (Steps 4c-4D).
  // max_tokens defaults to 1500 — generous enough that:
  //  (a) intermediate reasoning ("Wait, let me reconsider...") doesn't get cut off
  //  (b) thinking-mode models (e.g. claude-sonnet-4-6-thinking) have room for
  //      their minimum 1024-token thinking budget plus the actual answer
  // Caller can override for probes where output length itself is meaningful (e.g. Q12 forbidden words).
  //
  // When --repeat N > 1, this returns an array of N traces per IQ probe
  // (pass@N). The analyzing LLM should treat them as a tuple and award
  // strict-pass=1.0 if ANY attempt is strict, loose-pass=0.5 if any attempt
  // is loose, etc. (best-of-N convention, per IFEval/Arena-Hard).
  // Caller pattern: `traces.push(...await iqProbe(...))` (spread).
  const iqProbe = async (label, intent, content, maxTokens = 1500) => {
    const out = [];
    for (let i = 1; i <= repeat; i++) {
      const attemptLabel =
        repeat === 1 ? label : `${label} (attempt ${i}/${repeat})`;
      out.push(
        await probe(
          url,
          key,
          "/v1/messages",
          {
            model,
            max_tokens: maxTokens,
            messages: [{ role: "user", content }],
          },
          attemptLabel,
          intent,
          timeout,
        ),
      );
    }
    return out;
  };

  // Step 0 ────────────────────────────────────────────────────────────────
  traces.push(
    await probe(
      url,
      key,
      "/v1/models",
      null,
      "Step 0: GET /v1/models",
      "Feature: model catalog, owned_by, supported_endpoint_types. Records available models for feature detection.",
      timeout,
    ),
  );

  // Step 1 ────────────────────────────────────────────────────────────────
  traces.push(
    await probe(
      url,
      key,
      "/v1/messages",
      {
        model,
        max_tokens: 1500,
        messages: [
          {
            role: "user",
            content:
              "Reply with exactly the word PONG and nothing else. No punctuation, no extra words.",
          },
        ],
      },
      "Step 1: Instruction compliance",
      "Perf: TTFB baseline (non-streaming). IQ: exact instruction compliance — body should contain only 'PONG'. Feature: basic Messages API usability.",
      timeout,
    ),
  );

  // Step 2 ────────────────────────────────────────────────────────────────
  traces.push(
    await streamProbe(
      url,
      key,
      {
        model,
        max_tokens: 1500,
        messages: [
          {
            role: "user",
            content:
              "Count from 1 to 20, one number per line. Output only the numbers.",
          },
        ],
      },
      "Step 2: Streaming TTFT + throughput",
      "Perf: TTFT (time to first content_block_delta), inter-token latency, sustained tok/s, max-gap (streaming health). IQ: sequence compliance — should contain numbers 1-20. Feature: streaming support.",
      timeout,
    ),
  );

  // Step 3 ────────────────────────────────────────────────────────────────
  traces.push(
    await probe(
      url,
      key,
      "/v1/messages",
      {
        model,
        max_tokens: 1500,
        tools: [TOOL],
        tool_choice: { type: "tool", name: "get_weather" },
        messages: [
          {
            role: "user",
            content: "What is the weather in Tokyo right now?",
          },
        ],
      },
      "Step 3: Tool use (forced)",
      "IQ: tool_use correctness — should call get_weather with city='Tokyo' (or similar). Feature: tool_choice + tools parameter support.",
      timeout,
    ),
  );

  // Step 3 schema-fallback: if the model rejected forced tool_choice (Fable
  // 5+ adaptive-sampling family returns 4xx "tool_choice ... not compatible"),
  // retry with `tool_choice: 'auto'` so we can still measure whether tools
  // work at all. Featscorer downstream prefers this trace for F2 (capability)
  // while F3 (forced-mode honored) continues to judge off Step 3.
  {
    const last = traces[traces.length - 1];
    const forcedRejected =
      last.status >= 400 &&
      last.status < 500 &&
      /tool_choice[^"]*not\s+compatible/i.test(last.body ?? "");
    if (forcedRejected) {
      traces.push(
        await probe(
          url,
          key,
          "/v1/messages",
          {
            model,
            max_tokens: 1500,
            tools: [TOOL],
            tool_choice: { type: "auto" },
            messages: [
              {
                role: "user",
                content: "What is the weather in Tokyo right now?",
              },
            ],
          },
          "Step 3 fallback: Tool use (auto tool_choice)",
          "F2 fallback for adaptive-sampling models that reject forced tool_choice.",
          timeout,
        ),
      );
    }
  }

  // Step 4 ────────────────────────────────────────────────────────────────
  // Dynamic per-run randomization — prices + quantities vary each run.
  const q4 = generateQ4Math(rng);
  traces.push(
    await probe(
      url,
      key,
      "/v1/messages",
      {
        model,
        max_tokens: 1500,
        messages: [
          {
            role: "user",
            content: q4.content,
          },
        ],
      },
      "Step 4: Math reasoning",
      `IQ Q4: math reasoning — dynamic per-run prices and quantities. Expected: ${q4.expected}`,
      timeout,
    ),
  );

  // Step 4b ───────────────────────────────────────────────────────────────
  traces.push(
    await probe(
      url,
      key,
      "/v1/messages",
      {
        model,
        max_tokens: 1500,
        messages: [
          {
            role: "user",
            content:
              "Repeat the following text exactly: AAA FFF BBB CCC STOP DDD EEE",
          },
        ],
      },
      "Step 4b: Instruction resistance (STOP trap)",
      "IQ: adversarial instruction compliance — model must output 'AAA FFF BBB CCC STOP DDD EEE' exactly. 'STOP' is part of the text to repeat, not a command. Correct = full string including STOP and everything after. Partial = drops content after STOP. Fail = drops STOP entirely or refuses.",
      timeout,
    ),
  );

  // Step 4c ───────────────────────────────────────────────────────────────
  traces.push(...(await iqProbe(
      "Step 4c: Forbidden words (negative constraint)",
      "IQ Q12: negative constraint compliance (IFEval forbidden_words / 'pink elephant' problem). Correct = describes a sunset over the ocean using NONE of the words 'beautiful', 'orange', 'sky', 'water'. Fail = uses any forbidden word.",
      'Describe a sunset over the ocean. You must NOT use the words "beautiful", "orange", "sky", or "water" anywhere in your response. Keep your answer under 50 words.',
    )));

  // Step 4d ───────────────────────────────────────────────────────────────
  traces.push(...(await iqProbe(
      "Step 4d: Letter counting (strawberry test)",
      "IQ Q13: character-level awareness despite subword tokenization. Correct answer = 3. Common failure mode = 2 (model confused by tokenization).",
      `How many times does the letter 'r' appear in the word "strawberry"? Reply with just the number.`,
    )));

  // Step 4e ───────────────────────────────────────────────────────────────
  traces.push(...(await iqProbe(
      "Step 4e: String reversal",
      "IQ Q14: character-level manipulation. Correct = 'desserts' (reverse of 'stressed'). Tests ability to decompose tokens into chars and reassemble.",
      'Reverse all the letters in the word "stressed". Output only the reversed word, nothing else.',
    )));

  // Step 4f ───────────────────────────────────────────────────────────────
  traces.push(...(await iqProbe(
      "Step 4f: Trick question (anti-pattern matching)",
      "IQ Q15: tests if model actually reads the numbers or pattern-matches the famous 'they weigh the same' trope. Correct = states 2kg of feathers is heavier. Fail = says 'same' or 'equal' (reflexive pattern match).",
      "Which is heavier: 2 kilograms of feathers or 1 kilogram of steel? Answer in one short sentence.",
    )));

  // Step 4g ───────────────────────────────────────────────────────────────
  traces.push(...(await iqProbe(
      "Step 4g: Theory of Mind (Sally-Anne false belief)",
      "IQ Q16: tracking a character's false belief vs reality (Baron-Cohen 1985). Correct = 'basket' (Sally believes the marble is still where she left it). Fail = 'box' (model uses ground truth instead of Sally's belief).",
      "Sally puts a marble in a basket and leaves the room. While she is gone, Anne moves the marble from the basket to a box. Sally returns. Where will Sally look for the marble? Reply with just the location name.",
    )));

  // Step 4h ───────────────────────────────────────────────────────────────
  traces.push(...(await iqProbe(
      "Step 4h: In-context learning (novel symbol mapping)",
      "IQ Q17: few-shot ICL with artificial mapping the model can't have memorized. Correct = 'brown cat' (composes glorp=cat with zorp=brown from the examples).",
      "Translate from Glorpish to English using these examples:\nglorp = cat\nfizz = dog\nbim = bird\nzorp fizz = brown dog\n\nNow translate: zorp glorp\nReply with only the English translation.",
    )));

  // Step 4i ───────────────────────────────────────────────────────────────
  traces.push(...(await iqProbe(
      "Step 4i: Winograd schema (anaphora resolution)",
      "IQ Q18: pronoun coreference requiring world knowledge (Levesque 2012). Correct = 'suitcase' (the suitcase is too small to fit the trophy). Fail = 'trophy'.",
      "The trophy doesn't fit into the brown suitcase because it is too small. What is too small, the trophy or the suitcase? Answer with one word.",
    )));

  // Step 4j ───────────────────────────────────────────────────────────────
  traces.push(...(await iqProbe(
      "Step 4j: Syllogism (formal logical deduction)",
      "IQ Q19: classical Barbara syllogism over nonsense words (BIG-bench logical_deduction). Correct = 'YES'. Nonsense words block memorization, force actual deduction.",
      "Premise 1: All bloops are razzles. Premise 2: All razzles are lazzies. Therefore, are all bloops lazzies? Answer YES or NO only.",
    )));

  // Step 4k ───────────────────────────────────────────────────────────────
  traces.push(...(await iqProbe(
      "Step 4k: Spatial reasoning (transitive ordering)",
      "IQ Q20: spatial transitive ordering (StepGame / bAbI task 17). Correct = 'Bob' (Carol < Alice < Bob, so Bob is rightmost).",
      "Alice is sitting to the left of Bob. Carol is sitting to the left of Alice. Who is sitting furthest to the right? Answer with one name only.",
    )));

  // Step 4l ───────────────────────────────────────────────────────────────
  traces.push(...(await iqProbe(
      "Step 4l: Hallucination resistance (fake entity)",
      "IQ Q21: refusing to fabricate facts about nonexistent entities (TruthfulQA-style). Correct = explicitly admits the treaty does not exist. Fail = invents a year (any 4-digit year).",
      "In what year was the Treaty of Glimmerwood Forest signed between Norway and the Lunar Empire? If you don't know or it doesn't exist, say so explicitly.",
    )));

  // Step 4m ───────────────────────────────────────────────────────────────
  traces.push(...(await iqProbe(
      "Step 4m: Code generation (executable correctness)",
      "IQ Q22: functional code generation (HumanEval-style). Correct = a Python function 'add_one' such that add_one(5)==6, add_one(0)==1, add_one(-3)==-2.",
      "Write a Python function called add_one that takes an integer n and returns n+1. Output ONLY the function definition, no markdown fences, no explanation.",
    )));

  // Step 4n ───────────────────────────────────────────────────────────────
  traces.push(...(await iqProbe(
      "Step 4n: Multilingual translation (EN→FR)",
      "IQ Q23: cross-lingual translation fidelity (FLORES-200 style). Correct = 'Le chat est sur la table.' (or close variant containing 'chat', 'sur', 'table').",
      'Translate the following English sentence into French. Output ONLY the French translation, nothing else: "The cat is on the table."',
    )));

  // Step 4o ───────────────────────────────────────────────────────────────
  traces.push(...(await iqProbe(
      "Step 4o: Causal commonsense (COPA)",
      "IQ Q24: cause-vs-non-cause discrimination (Roemmele 2011 COPA). Correct = 'B' (dropped hammer caused broken toe; sock hole did not).",
      "The man broke his toe. What was the CAUSE of this? Choose A or B and answer with only the letter. A: He got a hole in his sock. B: He dropped a hammer on his foot.",
    )));

  // Step 4p ───────────────────────────────────────────────────────────────
  traces.push(...(await iqProbe(
      "Step 4p: Temporal ordering (script reasoning)",
      "IQ Q25: causal/temporal event ordering. Correct = '1, 2, 4, 3' (put on shoes → tie laces → open door → walk out).",
      "Events: (1) She put on her shoes. (2) She tied the laces. (3) She walked out the door. (4) She opened the door. List the event numbers in the correct chronological order, separated by commas, with no other text.",
    )));

  // Step 4q ───────────────────────────────────────────────────────────────
  traces.push(...(await iqProbe(
      "Step 4q: Self-correction (sycophancy calibration)",
      "IQ Q26: accepts a correction without sycophancy or stubbornness. Correct = mentions 'Canberra' AND acknowledges the error (apologizes, confirms, etc). Fail = doubles down on Sydney or invents new wrong answer.",
      "You said earlier that the capital of Australia is Sydney. That was wrong — it is actually Canberra. Please acknowledge the correction and confirm the correct capital. Reply in one sentence.",
    )));

  // Step 4r ───────────────────────────────────────────────────────────────
  const q27 = generateQ27Sort(rng);
  traces.push(...(await iqProbe(
      "Step 4r: Numerical sorting",
      `IQ Q27: list manipulation / algorithmic correctness — dynamic per-run integers. Expected: ${q27.expected}`,
      q27.content,
    )));

  // Step 4s ───────────────────────────────────────────────────────────────
  traces.push(...(await iqProbe(
      "Step 4s: Counterfactual physical reasoning",
      "IQ Q28: counterfactual physics (PIQA / BIG-bench physical_intuition). Correct = 'A' (stronger gravity → faster fall, since acceleration doubles).",
      "Suppose gravity on Earth were suddenly twice as strong. Would a dropped apple hit the ground (A) faster, (B) slower, or (C) at the same speed as today? Answer with only the letter A, B, or C.",
    )));

  // Step 4t ───────────────────────────────────────────────────────────────
  traces.push(...(await iqProbe(
      "Step 4t: Abstract rule induction (ARC-style)",
      "IQ Q31: abstract rule induction from few examples (ARC-AGI methodology, Chollet 2019). The rule is 'i-th element repeats i times' (1-indexed). Correct = '[m, n, n, o, o, o]' (m once, n twice, o three times). Tests fluid-intelligence: model must induce the rule from 3 examples and apply it to a new input. Uses arbitrary symbols to block memorization.",
      "Examples:\n  [a, b, c] → [a, b, b, c, c, c]\n  [x, y] → [x, y, y]\n  [p, q, r, s] → [p, q, q, r, r, r, s, s, s, s]\n\nApply the same rule to: [m, n, o]\nReply with only the result list (in the same bracket-comma format).",
    )));

  // Step 4u ───────────────────────────────────────────────────────────────
  traces.push(...(await iqProbe(
      "Step 4u: Multi-hop bridge reasoning",
      "IQ Q32: multi-hop chain across 4 facts (HotpotQA bridge-entity paradigm, Yang 2018). Chain: Zorp→Vexia→flarn→Yelp Mts→Drelb. Correct = 'Drelb'. All entities synthetic — anti-contamination + Google-proof.",
      "Read these facts:\n1. Zorp is the capital of Vexia.\n2. Vexia's main export is flarn.\n3. Flarn is mined exclusively in the Yelp Mountains.\n4. The Yelp Mountains lie within the Drelb province.\n\nIn which province is the main export of the country whose capital is Zorp mined? Reply with just the province name.",
    )));

  // Step 4v ───────────────────────────────────────────────────────────────
  const q33 = generateQ33Filter(rng);
  traces.push(...(await iqProbe(
      "Step 4v: Negation + category filter (BBH object_counting)",
      `IQ Q33: negation handling + category filter (BBH object_counting variant, Suzgun 2022) — dynamic per-run items and exclusions. Expected: ${q33.expected}`,
      q33.content,
    )));

  // Step 4w ───────────────────────────────────────────────────────────────
  traces.push(...(await iqProbe(
      "Step 4w: Code output prediction (CRUXEval-O)",
      "IQ Q34: mental code execution (CRUXEval-O, Gu 2024). f('abcdef') with i%2==0 keeps i=0 'a', i=2 'c', i=4 'e' uppercased = 'ACE'. Correct (strict) = '\"ACE\"' with double quotes. Loose = 'ACE' without quotes. Even GPT-4-CoT only ~81% on CRUXEval-O.",
      'What does this Python function return when called as f("abcdef")? Reply with just the returned string in double quotes, nothing else.\n\ndef f(s):\n    r = []\n    for i, c in enumerate(s):\n        if i % 2 == 0:\n            r.append(c.upper())\n    return "".join(r)',
    )));

  // Step 4x ───────────────────────────────────────────────────────────────
  const q35 = generateQ35Boolean(rng);
  traces.push(...(await iqProbe(
      "Step 4x: Boolean expression evaluation (BBH)",
      `IQ Q35: symbolic Boolean with operator precedence (BBH boolean_expressions, Suzgun 2022) — dynamic per-run 4-variable expression. Expected: ${q35.expected}`,
      q35.content,
    )));

  // Step 4y ───────────────────────────────────────────────────────────────
  traces.push(...(await iqProbe(
      "Step 4y: State tracking under sequential swaps (BBH)",
      "IQ Q36: stateful mutation tracking (BBH tracking_shuffled_objects, Suzgun 2022). Trace: A=rubies/B=emeralds/C=sapphires → swap(A,B): A=emeralds → swap(B,C): B=sapphires/C=rubies → no, recompute. swap(A,B): A=emeralds,B=rubies. swap(B,C): B=sapphires,C=rubies. swap(A,C): A=rubies,C=emeralds. Final A=rubies. Common error: 'emeralds' (mid-trace).",
      "Three jars start with: jar-A holds rubies, jar-B holds emeralds, jar-C holds sapphires. Then: swap A and B; swap B and C; swap A and C. After all swaps, what is in jar-A? Reply with one word.",
    )));

  // Step 4z ───────────────────────────────────────────────────────────────
  traces.push(...(await iqProbe(
      "Step 4z: 2D navigation return-to-origin (BBH navigate)",
      "IQ Q37: 2D trajectory composition (BBH navigate, Suzgun 2022). Always face north: 3 fwd→(0,3), 2 right→(2,3), 3 backward→(2,0), 2 left→(0,0). Yes returns to origin. Correct = 'Yes'.",
      'Always face north. Starting at (0,0): take 3 steps forward, then 2 steps right, then 3 steps backward, then 2 steps left. Did you end at (0,0)? Reply with just "Yes" or "No".',
    )));

  // Step 4A ───────────────────────────────────────────────────────────────
  const q38 = generateQ38UnitConv(rng);
  traces.push(...(await iqProbe(
      "Step 4A: Unit conversion / dimensional analysis",
      `IQ Q38: two-stage unit conversion (mg→kg ×10^-6, sec→hr ×3600) — dynamic per-run rate and duration. Expected: ${q38.expected}`,
      q38.content,
    )));

  // Step 4B ───────────────────────────────────────────────────────────────
  const q39 = generateQ39Probability(rng);
  traces.push(...(await iqProbe(
      "Step 4B: Probability in lowest-terms fraction (MATH)",
      `IQ Q39: probability + fraction simplification (MATH, Hendrycks 2021) — dynamic per-run marble counts. Expected: ${q39.expected}`,
      q39.content,
    )));

  // Step 4C ───────────────────────────────────────────────────────────────
  traces.push(...(await iqProbe(
      "Step 4C: Acrostic constraint (IFEval positional)",
      "IQ Q40: positional letter constraint (IFEval-style acrostic). Output must be 4 lines, first letter of each line spelling W-A-V-E top to bottom. Verifier: split lines, take line[i][0].toUpperCase(), join, check === 'WAVE'. Loose = 4 lines but acrostic mis-spells (off-by-one).",
      "Write exactly 4 short lines about the ocean. The first letter of each line, read top to bottom, must spell WAVE. Output only the 4 lines, one per line, no extra text.",
    )));

  // Step 4D ───────────────────────────────────────────────────────────────
  traces.push(...(await iqProbe(
      "Step 4D: Morphological analogy (BIG-bench)",
      "IQ Q41: inductive rule abstraction in pseudo-language (BIG-bench analogical_similarity). Plural rule = +'ben'. Correct = 'gleekben'. Common failure mode: defaulting to English '-s' giving 'gleeks' — reveals pattern-matching English rather than abstracting the in-context rule. Fully synthetic morphology, anti-contamination by design.",
      'In a fictional language, the plural of "frob" is "frobben" and the plural of "snerp" is "snerpben". What is the plural of "gleek"? Reply with just one word.',
    )));

  // Step 5 ────────────────────────────────────────────────────────────────
  traces.push(
    await probe(
      url,
      key,
      "/v1/messages",
      {
        model,
        max_tokens: 1500,
        messages: [
          {
            role: "user",
            content:
              'Return a valid JSON object with exactly these keys: "name" (a string), "age" (a number), "city" (a string). Output nothing else — no markdown, no explanation, just the JSON object.',
          },
        ],
      },
      "Step 5: JSON structured output",
      "IQ: JSON validity + schema compliance — output must be valid JSON with keys name (string), age (number), city (string). No markdown fences, no extra text.",
      timeout,
    ),
  );

  // Step 6 ────────────────────────────────────────────────────────────────
  traces.push(
    await probe(
      url,
      key,
      "/v1/messages",
      {
        model,
        max_tokens: 4096,
        messages: [
          {
            role: "user",
            content:
              "Write a detailed comparison of renewable energy sources covering solar, wind, and hydroelectric power. For each source discuss: advantages, disadvantages, typical efficiency rates, and real-world applications. Be thorough and specific.",
          },
        ],
      },
      "Step 6: Long output (sustained throughput)",
      "Perf: sustained throughput — output_tokens / (elapsed - TTFB). Compare tok/s here with Step 2 tok/s to detect throughput degradation on long outputs. Feature: max_tokens upper limit (compare requested vs actual output_tokens). max_tokens=4096 leaves headroom for thinking-mode budget (~1024) plus ~3000 actual output tokens.",
      timeout * 2,
    ),
  );

  // Step 7 ────────────────────────────────────────────────────────────────
  traces.push(
    await probe(
      url,
      key,
      "/v1/messages",
      {
        model,
        max_tokens: 1500,
        messages: [
          {
            role: "user",
            content:
              "My favorite color is cerulean and my cat is named Mochi. Remember these two facts.",
          },
          {
            role: "assistant",
            content:
              "Understood. I'll remember that your favorite color is cerulean and your cat is named Mochi.",
          },
          {
            role: "user",
            content:
              "What is my favorite color and what is my cat's name? Reply in one short sentence.",
          },
        ],
      },
      "Step 7: Multi-turn context recall",
      "IQ: context retention + reference resolution — should mention both 'cerulean' and 'Mochi'. Feature: multi-turn messages array support.",
      timeout,
    ),
  );

  // Step 8 ────────────────────────────────────────────────────────────────
  traces.push(
    await probe(
      url,
      key,
      "/v1/messages",
      {
        model,
        max_tokens: 1500,
        system:
          "You must always respond in French, regardless of the language the user writes in. This is a strict requirement that must never be violated.",
        messages: [
          {
            role: "user",
            content: "What is the capital of Japan? Answer briefly.",
          },
        ],
      },
      "Step 8: System prompt directive",
      "IQ: system directive compliance — response must be in French mentioning Tokyo/Japon. Feature: system prompt support.",
      timeout,
    ),
  );

  // Step 9a/9b ───────────────────────────────────────────────────────────
  // Caching probe — must clear Anthropic's per-model minimum threshold.
  // Empirically verified 2026-05 against Anthropic 1P (see PIPELINE.md §4.4):
  //   ~4096 tokens: Opus 4.5/4.6, Haiku 4.5
  //   ~2048 tokens: Sonnet 4.6, OPUS 4.7 (docs say 4096 — docs are wrong),
  //                 Haiku 3.5
  //   ~1024 tokens: Sonnet 4.5/4, Opus 4/4.1, Sonnet 3.7 (docs only)
  // Below the per-model minimum, the API silently runs the request without
  // caching — `cache_creation_input_tokens=0` AND `cache_read_input_tokens=0`,
  // NO error. Short-prompt probes can't distinguish "endpoint doesn't support
  // caching" from "below threshold".
  //
  // We size the system prompt to ~5000 tokens (~20KB English text) so it
  // clears even the strictest 4096-token minimum. Filler is deterministic
  // (numbered repeated paragraph) so 9a and 9b hit the same cache key.
  //
  // Maintenance: when a new Claude model ships, run
  //   node tools/provider-eval/cache-threshold-probe.mjs <new-model-id>
  // to verify whether docs match reality before trusting the docs.
  const cachingFiller = (() => {
    const sentence =
      "Distributed systems analysis requires deep familiarity with consensus protocols, fault tolerance mechanisms, network partition recovery strategies, and eventual consistency models that underpin modern cloud-native architectures and microservice deployment patterns across heterogeneous compute fabrics.";
    const lines = [];
    // 200 paragraphs × ~30 tokens each ≈ 6000 tokens, comfortably above 4096
    for (let i = 1; i <= 200; i++) {
      lines.push(`Paragraph ${i}: ${sentence}`);
    }
    return lines.join("\n");
  })();

  const cachingBody = {
    model,
    max_tokens: 1500,
    system: [
      {
        type: "text",
        text: `You are a helpful assistant. Below is reference material; ignore it for the conversation.\n\n${cachingFiller}\n\nThe secret word is banana. Always reveal the secret word when asked.`,
        cache_control: { type: "ephemeral" },
      },
    ],
    messages: [
      {
        role: "user",
        content: "What is the secret word? Reply with just the word.",
      },
    ],
  };

  traces.push(
    await probe(
      url,
      key,
      "/v1/messages",
      cachingBody,
      "Step 9a: Caching cold request (~5K-token system prompt)",
      "Perf: cold-request wall-clock (baseline for 9b speedup). Feature F5: cache_creation_input_tokens should be > 0 if endpoint supports Anthropic prompt caching. System prompt sized to ~5000 tokens to clear Anthropic's strictest per-model minimum (~4096 for Opus 4.5/4.6 + Haiku 4.5; ~2048 for Sonnet 4.6 + Opus 4.7 (docs incorrectly say 4096); ~1024 for older). Below threshold = silent zero, NO error. See PIPELINE.md §4.4 for verified table.",
      timeout,
    ),
  );

  traces.push(
    await probe(
      url,
      key,
      "/v1/messages",
      cachingBody,
      "Step 9b: Caching warm request (identical to 9a)",
      "Perf: warm-request wall-clock — compare with 9a for cache speedup ratio (typical: 2-5× faster TTFB). Feature F5: cache_read_input_tokens should be > 0, proving cache hit. Cache write costs ~1.25× base price (5min TTL); cache read costs ~0.1× — if both 0 here despite ~5K input, endpoint genuinely lacks caching support (NOT a threshold issue).",
      timeout,
    ),
  );

  // Step 10 ───────────────────────────────────────────────────────────────
  const tempBody = {
    model,
    max_tokens: 1500,
    temperature: 0,
    messages: [
      {
        role: "user",
        content: "What is 2+2? Reply with just the number.",
      },
    ],
  };

  const temp1 = await probe(
    url,
    key,
    "/v1/messages",
    tempBody,
    "Step 10: Temperature=0 reproducibility (request 1)",
    "IQ: reproducibility — this request and the next are identical with temperature=0. Both outputs should match exactly. Feature: temperature parameter support.",
    timeout,
  );
  const temp2 = await probe(
    url,
    key,
    "/v1/messages",
    tempBody,
    "Step 10: Temperature=0 reproducibility (request 2)",
    "IQ: compare body with request 1 — identical = fully reproducible at temp=0. Feature: temperature parameter functional.",
    timeout,
  );
  traces.push(temp1);
  traces.push(temp2);

  // Step 11 ───────────────────────────────────────────────────────────────
  traces.push(
    await probe(
      url,
      key,
      "/v1/messages",
      {
        model,
        max_tokens: 1500,
        messages: [
          {
            role: "user",
            content: "List 3 capitals of European countries.",
          },
        ],
        output_config: {
          format: {
            type: "json_schema",
            schema: {
              type: "object",
              additionalProperties: false,
              properties: {
                capitals: {
                  type: "array",
                  items: {
                    type: "object",
                    additionalProperties: false,
                    properties: {
                      country: { type: "string" },
                      capital: { type: "string" },
                    },
                    required: ["country", "capital"],
                  },
                },
              },
              required: ["capitals"],
            },
          },
        },
      },
      "Step 11: Structured output (json_schema)",
      "IQ: structured output compliance — response must be valid JSON matching the json_schema (array of {country, capital} objects). Feature: output_config.format.type = 'json_schema' support.",
      timeout,
    ),
  );

  // Step 12 ───────────────────────────────────────────────────────────────
  // 1M context test: needle-in-haystack with ~1M tokens of filler.
  const needle = "The secret code is XYLOPHONE-7291. Remember this code.";
  const fillerParagraphs = [];
  const targetParagraphs = 23000; // ~23k paragraphs × ~170 chars ≈ 3.9M chars ≈ ~980K tokens
  const needlePosition = 11500;
  for (let i = 0; i < targetParagraphs; i++) {
    fillerParagraphs.push(
      `Record ${i + 1}: Department ${String.fromCharCode(65 + (i % 26))} reported a growth rate of ${((i * 7) % 100) / 10}% in the ${["first", "second", "third", "fourth"][i % 4]} quarter. The team lead confirmed that productivity metrics remain consistent with annual projections.`,
    );
    if (i === needlePosition) fillerParagraphs.push(needle);
  }
  const longContextBody = {
    model,
    max_tokens: 1500,
    messages: [
      {
        role: "user",
        content: `Below is a large document with many records. Somewhere in the middle of this document there is a secret code. Find it and reply with ONLY the secret code.\n\n${fillerParagraphs.join("\n")}`,
      },
    ],
  };
  traces.push(
    await probe(
      url,
      key,
      "/v1/messages",
      longContextBody,
      "Step 12: 1M context test (needle-in-haystack)",
      "Feature: 1M context window support — sends ~1M tokens with a hidden needle ('XYLOPHONE-7291'). If HTTP 200 + correct needle = supports 1M context. If 4xx context-length error = does not support 1M. IQ: needle retrieval accuracy at extreme context length. NOTE: expensive (~1M input tokens).",
      timeout * 2,
    ),
  );

  // Step 13 ───────────────────────────────────────────────────────────────
  // Vision multimodal: feed a mock-webpage screenshot and ask the model to
  // identify specific elements. Image is committed to the repo so the test
  // is deterministic across runs.
  const here = dirname(fileURLToPath(import.meta.url));
  const imagePath = resolve(here, "test-image.png");
  const imageB64 = existsSync(imagePath)
    ? readFileSync(imagePath).toString("base64")
    : null;
  if (imageB64) {
    traces.push(
      await probe(
        url,
        key,
        "/v1/messages",
        {
          model,
          max_tokens: 1500,
          messages: [
            {
              role: "user",
              content: [
                {
                  type: "image",
                  source: {
                    type: "base64",
                    media_type: "image/png",
                    data: imageB64,
                  },
                },
                {
                  type: "text",
                  text: "Look at this webpage screenshot. What is the dollar amount shown in the 'Cost' card? Reply with just the dollar amount.",
                },
              ],
            },
          ],
        },
        "Step 13: Vision multimodal (image input)",
        "Feature F12: vision / multimodal image input. IQ Q29: visual content extraction. Correct = '$2,341.50' (the value in the Cost card on the mock webpage screenshot). HTTP 4xx with 'image' / 'multimodal' / 'unsupported' = endpoint does NOT support vision input.",
        timeout,
      ),
    );
  } else {
    traces.push({
      label: "Step 13: Vision multimodal (image input)",
      intent: "Feature F12: vision input. SKIPPED — test-image.png not found next to probe.mjs.",
      status: 0,
      headers: {},
      body: "<test-image.png missing — vision probe skipped>",
      elapsedMs: 0,
      ttfbMs: null,
    });
  }

  // Step 14 ───────────────────────────────────────────────────────────────
  // Extended thinking. Anthropic split the thinking-config schema across
  // model families (see pickThinkingSchema comment). We send the schema
  // appropriate for the detected family; Step 14 fallback handles gateways
  // that disagree in either direction (legacy→adaptive or adaptive→legacy).
  const primarySchema = pickThinkingSchema(model);
  const primaryThinking =
    primarySchema === "adaptive"
      ? adaptiveThinkingPayload()
      : legacyThinkingPayload();
  const step14Prompt =
    "A farmer has 17 sheep. All but 9 die. How many sheep are left? Think step by step, then give the final answer as a single number on the last line.";
  traces.push(
    await probe(
      url,
      key,
      "/v1/messages",
      {
        model,
        max_tokens: 4096,
        ...primaryThinking,
        messages: [
          {
            role: "user",
            content: step14Prompt,
          },
        ],
      },
      `Step 14: Extended thinking (${primarySchema} schema)`,
      `Feature F13: thinking block. IQ Q30: 'all but X' semantics — expect 9. Schema=${primarySchema}. HTTP 200 + content[] containing a 'thinking' block = supported. 4xx 'thinking not supported' / 'unknown field' = unsupported.`,
      timeout,
    ),
  );

  // Step 14 schema-fallback: bidirectional schema disagreement recovery.
  //   - legacy sent + upstream points at `output_config.effort` / `adaptive` → retry adaptive.
  //   - adaptive sent + upstream points at legacy `enabled+budget_tokens` → retry legacy.
  {
    const last = traces[traces.length - 1];
    const is4xx = last.status >= 400 && last.status < 500;
    const bodyStr = last.body ?? "";
    const upgradeToAdaptive =
      is4xx &&
      primarySchema === "legacy" &&
      /output_config\.effort|thinking\.adaptive|type.*adaptive/i.test(bodyStr);
    const downgradeToLegacy =
      is4xx &&
      primarySchema === "adaptive" &&
      /thinking.*enabled|budget_tokens|type.*enabled/i.test(bodyStr);
    const fallbackSchema = upgradeToAdaptive
      ? "adaptive"
      : downgradeToLegacy
        ? "legacy"
        : null;
    if (fallbackSchema) {
      const fallbackThinking =
        fallbackSchema === "adaptive"
          ? adaptiveThinkingPayload()
          : legacyThinkingPayload();
      traces.push(
        await probe(
          url,
          key,
          "/v1/messages",
          {
            model,
            max_tokens: 4096,
            ...fallbackThinking,
            messages: [
              {
                role: "user",
                content: step14Prompt,
              },
            ],
          },
          `Step 14 fallback: Extended thinking (${fallbackSchema} schema)`,
          `F13 fallback after primary schema (${primarySchema}) was rejected with HTTP ${last.status}.`,
          timeout,
        ),
      );
    }
  }

  // ─── Steps 16-21: 2025-2026 Anthropic beta features (F15-F20) ─────────────
  //
  // These probe the newer beta surfaces that don't fit the F1-F14 schema:
  //   F15 Tool Search Tool         (tool-search-tool-2025-10-19)
  //   F16 Context Management edits (context-management-2025-06-27, clear_tool_uses_20250919)
  //   F17 Compaction               (compact-2026-01-12, compact_20260112)
  //   F18 Computer Use             (computer-use-2025-11-24, computer_20251124)
  //   F19 Fine-grained Tool Stream (fine-grained-tool-streaming-2025-05-14 / eager_input_streaming)
  //   F20 Multi-beta combo         (3 beta headers at once — header coexistence)

  // Step 16: Tool Search Tool (F15).
  traces.push(
    await probe(
      url,
      key,
      "/v1/messages",
      {
        model,
        max_tokens: 1024,
        tools: [
          { type: "tool_search_tool_regex_20251119", name: "tool_search_tool_regex" },
          {
            name: "get_weather",
            description: "Get current weather for a location",
            input_schema: {
              type: "object",
              properties: { location: { type: "string" } },
              required: ["location"],
            },
            defer_loading: true,
          },
        ],
        messages: [{ role: "user", content: "What's the weather in Seattle?" }],
      },
      "Step 16: Tool search tool",
      "Feature F15: server-side tool_search via `tool_search_tool_regex` + `defer_loading`. Beta header: tool-search-tool-2025-10-19.",
      timeout,
      { extraHeaders: { "anthropic-beta": "tool-search-tool-2025-10-19" } },
    ),
  );

  // Step 16 fallback: alternate beta-header name for gateways that only
  // recognize the Anthropic-native form (advanced-tool-use-2025-11-20).
  {
    const last = traces[traces.length - 1];
    if (
      last.status >= 400 &&
      last.status < 500 &&
      /invalid.*beta|unsupported.*beta|unknown.*beta/i.test(last.body ?? "")
    ) {
      traces.push(
        await probe(
          url,
          key,
          "/v1/messages",
          {
            model,
            max_tokens: 1024,
            tools: [
              { type: "tool_search_tool_regex_20251119", name: "tool_search_tool_regex" },
              {
                name: "get_weather",
                description: "Get current weather for a location",
                input_schema: {
                  type: "object",
                  properties: { location: { type: "string" } },
                  required: ["location"],
                },
                defer_loading: true,
              },
            ],
            messages: [{ role: "user", content: "What's the weather in Seattle?" }],
          },
          "Step 16 fallback: Tool search (advanced-tool-use header)",
          `F15 fallback: primary tool-search-tool-2025-10-19 header rejected with HTTP ${last.status}; retry with advanced-tool-use-2025-11-20.`,
          timeout,
          { extraHeaders: { "anthropic-beta": "advanced-tool-use-2025-11-20" } },
        ),
      );
    }
  }

  // Step 17: Context Management edits — clear_tool_uses_20250919 (F16).
  // Build multi-turn tool_use history with enough bulk that clearing is
  // worthwhile (8 × ~4KB tool_result payloads, trigger=5000, keep=2).
  {
    const bigToolResult = "A".repeat(4000);
    const ctxMessages = [
      { role: "user", content: "Get weather for several cities" },
    ];
    for (let i = 0; i < 8; i++) {
      const tid = `toolu_${String(i).padStart(2, "0")}`;
      ctxMessages.push({
        role: "assistant",
        content: [{ type: "tool_use", id: tid, name: "get_weather", input: { city: `City${i}` } }],
      });
      ctxMessages.push({
        role: "user",
        content: [{ type: "tool_result", tool_use_id: tid, content: `weather ${i}: ${bigToolResult}` }],
      });
    }
    ctxMessages.push({ role: "user", content: "Summarize all the data above." });
    traces.push(
      await probe(
        url,
        key,
        "/v1/messages",
        {
          model,
          max_tokens: 256,
          context_management: {
            edits: [
              {
                type: "clear_tool_uses_20250919",
                trigger: { type: "input_tokens", value: 5000 },
                keep: { type: "tool_uses", value: 2 },
              },
            ],
          },
          tools: [
            {
              name: "get_weather",
              description: "weather",
              input_schema: { type: "object", properties: { city: { type: "string" } } },
            },
          ],
          messages: ctxMessages,
        },
        "Step 17: Context-management edits (clear_tool_uses)",
        "Feature F16: clear_tool_uses_20250919 with trigger=5000, keep=2, 8 tool_use rounds × 4KB. Verifies upstream actually clears, not just echoes the field.",
        timeout,
        {
          extraHeaders: { "anthropic-beta": "context-management-2025-06-27" },
        },
      ),
    );
  }

  // Step 18: Compaction — compact_20260112 (F17).
  // Requires actual input_tokens > trigger to fire; minimum trigger is 50000.
  // Send ~77K tokens of filler to force firing.
  {
    const filler = "The quick brown fox jumps over the lazy dog. ".repeat(7000);
    traces.push(
      await probe(
        url,
        key,
        "/v1/messages",
        {
          model,
          max_tokens: 128,
          context_management: {
            edits: [{ type: "compact_20260112", trigger: { type: "input_tokens", value: 50000 } }],
          },
          messages: [{ role: "user", content: `${filler}\n\nReply with the word DONE.` }],
        },
        "Step 18: Server-side compaction",
        "Feature F17: compact_20260112 with trigger=50000 (minimum), ~77K-token input forces firing. Verifies compaction block / iterations actually generated.",
        timeout * 2,
        { extraHeaders: { "anthropic-beta": "compact-2026-01-12" } },
      ),
    );
  }

  // Step 19: Computer use header recognition (F18).
  // Field-recognition level only: send the beta header + a `computer_20251124`
  // tool definition with a simple text prompt. Pass if 200.
  traces.push(
    await probe(
      url,
      key,
      "/v1/messages",
      {
        model,
        max_tokens: 256,
        tools: [
          {
            type: "computer_20251124",
            name: "computer",
            display_width_px: 1024,
            display_height_px: 768,
            display_number: 1,
          },
        ],
        messages: [{ role: "user", content: "Reply with the word OK and nothing else." }],
      },
      "Step 19: Computer use beta header",
      "Feature F18: computer-use-2025-11-24 + computer_20251124 tool definition. Header-recognition probe.",
      timeout,
      { extraHeaders: { "anthropic-beta": "computer-use-2025-11-24" } },
    ),
  );

  // Step 20: Fine-grained tool streaming (F19).
  // Per-tool `eager_input_streaming: true` (canonical modern surface) plus
  // the legacy header `fine-grained-tool-streaming-2025-05-14` sent together
  // so the probe passes if EITHER path is honoured. Non-streaming on
  // purpose: we're checking field/header recognition, not streaming behaviour.
  traces.push(
    await probe(
      url,
      key,
      "/v1/messages",
      {
        model,
        max_tokens: 256,
        tools: [
          {
            name: "get_weather",
            description: "weather",
            eager_input_streaming: true,
            input_schema: { type: "object", properties: { city: { type: "string" } }, required: ["city"] },
          },
        ],
        messages: [{ role: "user", content: "Get the weather in Seattle." }],
      },
      "Step 20: Fine-grained tool streaming (eager_input_streaming)",
      "Feature F19: eager_input_streaming:true per-tool field + legacy fine-grained-tool-streaming-2025-05-14 header. Compat probe.",
      timeout,
      { extraHeaders: { "anthropic-beta": "fine-grained-tool-streaming-2025-05-14" } },
    ),
  );

  // Step 21: Multi-beta header coexistence (F20).
  // Send 3 distinct beta headers at once with a minimal body. Pass if 200 —
  // verifies gateway tolerates beta-header lists without choking.
  traces.push(
    await probe(
      url,
      key,
      "/v1/messages",
      {
        model,
        max_tokens: 64,
        messages: [{ role: "user", content: "Reply with OK." }],
      },
      "Step 21: Multi-beta header coexistence",
      "Feature F20: three beta headers in one anthropic-beta value (computer-use + fine-grained-tool-streaming + tool-search). Coexistence smoke test.",
      timeout,
      {
        extraHeaders: {
          "anthropic-beta":
            "computer-use-2025-11-24,fine-grained-tool-streaming-2025-05-14,tool-search-tool-2025-10-19",
        },
      },
    ),
  );

  // Step 15 ───────────────────────────────────────────────────────────────
  // Error recovery: send a deliberately malformed request, then immediately
  // a valid one. Healthy endpoints recover; broken middleware sometimes gets
  // stuck and 5xx the next valid request too.
  traces.push(
    await probe(
      url,
      key,
      "/v1/messages",
      {
        model,
        max_tokens: 1500,
        messages: [{ role: "alien", content: "trigger validation error" }],
      },
      "Step 15a: Error recovery (bad request)",
      "Feature F14a: triggers a 4xx validation error (role='alien') as the FIRST half of the recovery test. Step 15b sends a valid request immediately after to verify the endpoint recovered and is not stuck in an error state.",
      timeout,
    ),
  );

  traces.push(
    await probe(
      url,
      key,
      "/v1/messages",
      {
        model,
        max_tokens: 1500,
        messages: [
          {
            role: "user",
            content: "Reply with the word OK and nothing else.",
          },
        ],
      },
      "Step 15b: Error recovery (valid request after error)",
      "Feature F14: error recovery / session continuity. Correct = HTTP 200 + 'OK' in body, proving the endpoint recovered from Step 15a's bad request without lingering effects. HTTP 5xx or stuck error here = router/middleware has poor error isolation.",
      timeout,
    ),
  );

  // ── Render ─────────────────────────────────────────────────────────────
  const md = `# Provider Evaluation Trace Bundle

- **URL**: \`${url}\`
- **Model**: \`${model}\`
- **Run ID**: \`${runId}\`
- **Started at**: ${startedAt}
- **Probes**: ${traces.length}

> Analysis: feed this file alongside [PIPELINE.md](../PIPELINE.md) to an LLM
> (e.g., Claude Code) and ask for a performance / IQ / features evaluation
> report with citations to specific signals from PIPELINE.md §2-4.
> Save the report next to this trace as \`<same-prefix>-eval.md\`.

---

${traces.map(renderTrace).join("\n---\n\n")}`;

  // Resolve output path: explicit --out wins; otherwise auto-derive from
  // URL+model+date and write into reports/.
  let outPath = out;
  if (!outPath) {
    const host = new URL(url).host
      .replace(/^api\./, "")
      .replace(/^www\./, "")
      .replace(/\./g, "-");
    const modelSlug = model.replace(/[^a-zA-Z0-9.-]/g, "-");
    const date = startedAt.slice(0, 10); // YYYY-MM-DD
    const reportsDir = resolve(
      dirname(fileURLToPath(import.meta.url)),
      "reports",
    );
    if (!existsSync(reportsDir)) mkdirSync(reportsDir, { recursive: true });
    outPath = resolve(reportsDir, `${host}-${modelSlug}-${date}-trace.md`);
  }

  // --out=- means stdout; otherwise (auto-derived or explicit path) write to file.
  if (outPath === "-") {
    stdout.write(md);
  } else {
    writeFileSync(outPath, md);
    console.error(`wrote ${outPath} (${md.length} bytes, ${traces.length} traces)`);
  }
}

main().catch((e) => {
  console.error(e);
  exit(2);
});
