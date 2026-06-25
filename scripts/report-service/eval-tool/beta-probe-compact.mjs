#!/usr/bin/env node
// Verify compact-2026-01-12 by crossing its 50000-token min trigger and
// inspecting whether the response shows compaction iterations.
//
// Signals that compaction actually fired (from docs):
//   - usage.iterations[] is present with >=1 entry
//   - response content may contain compaction summary blocks
//   - applied_edits[] may list compact_20260112
//
// Cost: ~55k input tokens × 2 calls ≈ $0.33 on Sonnet 4.6 (input only).

import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const __dirname = dirname(fileURLToPath(import.meta.url));
const envFile = (() => {
  const text = readFileSync(join(__dirname, ".env"), "utf8");
  const e = {};
  for (const line of text.split("\n")) {
    const m = line.match(/^([A-Z_]+)\s*=\s*(.*)$/);
    if (m) e[m[1]] = m[2].replace(/^["']|["']$/g, "").trim();
  }
  return e;
})();

const BASE = envFile.URL.replace(/\/$/, "");
const KEY = envFile.KEY;
const MODEL = process.env.MODEL_OVERRIDE || envFile.MODEL || "claude-sonnet-4-6";

// Build ~66k tokens of conversational filler. Empirically ~188 tok/paragraph.
function bigUserContent() {
  const paragraph = (i) =>
    `Section ${i}: Consider the design of distributed consensus protocols. Paxos, Raft, and Viewstamped Replication all share the core property of replicating a deterministic state machine across nodes while tolerating crash faults of a minority. The leader-based variants reduce the number of round trips required in the common case, but introduce additional complexity in leader election and lease management. Quorum intersection ensures that any two majority sets share at least one node, which preserves invariants across leader changes. Liveness is conditioned on partial synchrony; safety holds under arbitrary asynchrony. Practical deployments must contend with network partitions, message reordering, clock skew, and disk failures. Implementations like etcd, ZooKeeper, and Consul make different tradeoffs around read scalability, write throughput, and operator ergonomics.`;
  return Array.from({ length: 350 }, (_, i) => paragraph(i + 1)).join("\n\n");
}

async function fire(label, { withCompact, betaHeader }) {
  const body = {
    model: MODEL,
    max_tokens: 64,
    messages: [
      { role: "user", content: bigUserContent() },
      { role: "user", content: "Summarize the above in one sentence." },
    ],
  };
  if (withCompact) {
    body.context_management = {
      edits: [{
        type: "compact_20260112",
        trigger: { type: "input_tokens", value: 50000 },
      }],
    };
  }
  const headers = {
    "content-type": "application/json",
    "x-api-key": KEY,
    "anthropic-version": "2023-06-01",
  };
  if (betaHeader) headers["anthropic-beta"] = betaHeader;
  const t0 = Date.now();
  const res = await fetch(`${BASE}/v1/messages`, {
    method: "POST", headers, body: JSON.stringify(body),
  });
  const text = await res.text();
  let json; try { json = JSON.parse(text); } catch {}
  const ms = Date.now() - t0;
  console.log(`\n=== [${label}] status=${res.status}  ${ms}ms ===`);
  if (res.status !== 200) {
    console.log("err:", (json?.error?.message || text).slice(0, 300));
    return;
  }
  const u = json.usage || {};
  const cm = json.context_management;
  const content = json.content || [];
  const blockTypes = content.map(b => b.type);
  console.log("top-level keys:", Object.keys(json).join(", "));
  console.log("usage.input_tokens:", u.input_tokens, " output_tokens:", u.output_tokens);
  console.log("usage.iterations:", JSON.stringify(u.iterations));
  console.log("usage.cache_read:", u.cache_read_input_tokens ?? 0, " cache_creation:", u.cache_creation_input_tokens ?? 0);
  console.log("context_management:", JSON.stringify(cm));
  console.log("content block types:", blockTypes.join(", "));
  console.log("stop_reason:", json.stop_reason, " id:", json.id?.slice(0, 40));
}

console.log(`endpoint=${BASE}  model=${MODEL}`);
console.log(`payload target: ~55k tokens, trigger=50000`);

await fire("compact + beta", { withCompact: true, betaHeader: "compact-2026-01-12" });
await new Promise(r => setTimeout(r, 800));
await fire("baseline (no compact, no beta)", { withCompact: false, betaHeader: null });

console.log();
console.log("Diagnosis:");
console.log("  iterations[] populated OR applied_edits[] has compact_20260112  → compaction fired");
console.log("  iterations missing AND input_tokens ~= baseline                 → compaction silently ignored");
