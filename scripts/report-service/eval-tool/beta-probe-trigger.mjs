#!/usr/bin/env node
// Follow-up to beta-probe.mjs: actually TRIGGER clear_tool_uses_20250919 by
// constructing a multi-turn convo whose tool_result payloads exceed `trigger`.
// Compares response `context_management.applied_edits[]` and `usage.input_tokens`
// against a baseline run with no context_management to verify the gateway is
// not just echoing the field but actually invoking the server-side edit.
//
// Cost: ~5000 input tokens per call × 3 calls ≈ $0.05 on Sonnet 4.6.

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
const TRIGGER = Number(process.env.TRIGGER || 2000);
const ROUNDS = Number(process.env.ROUNDS || 6);

// One ~500-token chunk of filler "file content" per tool_result.
const filler = (i) =>
  `[snippet ${i}] ${Array.from({ length: 40 }, (_, k) =>
    `line${k + 1}: distributed-systems consensus ledger paxos raft quorum replication invariant safety liveness ${i}-${k}.`
  ).join("\n")}`;

function buildMessages() {
  // Force a multi-turn tool_use / tool_result sequence so there's something for
  // clear_tool_uses to actually clear.
  const messages = [{ role: "user", content: "Look up snippets 1..N using read_file." }];
  for (let i = 1; i <= ROUNDS; i++) {
    messages.push({
      role: "assistant",
      content: [
        { type: "text", text: `Reading snippet ${i}.` },
        {
          type: "tool_use",
          id: `toolu_fake_${String(i).padStart(2, "0")}`,
          name: "read_file",
          input: { path: `/snippet/${i}.txt` },
        },
      ],
    });
    messages.push({
      role: "user",
      content: [{
        type: "tool_result",
        tool_use_id: `toolu_fake_${String(i).padStart(2, "0")}`,
        content: filler(i),
      }],
    });
  }
  messages.push({ role: "user", content: "Summarize in one sentence." });
  return messages;
}

const tools = [{
  name: "read_file",
  description: "Read a file from disk.",
  input_schema: {
    type: "object",
    properties: { path: { type: "string" } },
    required: ["path"],
  },
}];

async function fire(label, { withCtxMgmt, betaHeader }) {
  const body = {
    model: MODEL,
    max_tokens: 64,
    tools,
    messages: buildMessages(),
  };
  if (withCtxMgmt) {
    body.context_management = {
      edits: [{
        type: "clear_tool_uses_20250919",
        trigger: { type: "input_tokens", value: TRIGGER },
        keep: { type: "tool_uses", value: 1 },
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
  const u = json?.usage || {};
  const cm = json?.context_management;
  const applied = cm?.applied_edits || [];
  console.log(`[${label.padEnd(28)}] ${res.status}  ${ms}ms`);
  if (res.status !== 200) {
    console.log(`   err: ${(json?.error?.message || text).slice(0, 200)}`);
    return;
  }
  console.log(`   usage.input_tokens=${u.input_tokens}  output_tokens=${u.output_tokens}  cache_read=${u.cache_read_input_tokens ?? 0}`);
  console.log(`   context_management echoed=${!!cm}  applied_edits=${applied.length}`);
  for (const e of applied) {
    console.log(`     • ${e.type}: cleared_tool_uses=${e.cleared_tool_uses ?? "?"}  cleared_input_tokens=${e.cleared_input_tokens ?? "?"}`);
  }
}

console.log(`endpoint=${BASE}  model=${MODEL}  trigger=${TRIGGER}  rounds=${ROUNDS}`);
console.log();

await fire("baseline (no ctx_mgmt)",   { withCtxMgmt: false, betaHeader: null });
await new Promise(r => setTimeout(r, 500));
await fire("ctx_mgmt + beta",          { withCtxMgmt: true,  betaHeader: "context-management-2025-06-27" });
await new Promise(r => setTimeout(r, 500));
await fire("ctx_mgmt no-beta",         { withCtxMgmt: true,  betaHeader: null });

console.log();
console.log("Diagnosis:");
console.log("  applied_edits.length >= 1   → upstream actually invoked clear_tool_uses");
console.log("  applied_edits.length == 0   → trigger not hit (raise ROUNDS) OR gateway echoes field but ignores it");
console.log("  ctx_mgmt+beta and no-beta both show applied>=1  → gateway auto-injects the beta header");
