#!/usr/bin/env node
/**
 * 根据 ID→Key 映射，从 API 获取每个渠道的已用额度，写回 CSV 的"实际消耗"列
 *
 * 用法:
 *   node match_key_usage.mjs <mapping_file> <csv_file>
 *
 * 环境变量:
 *   POLARCODE_URL   - 服务地址 (默认: https://www.punkcode.cc)
 *   POLARCODE_TOKEN - 管理员 Access Token (必填)
 *   POLARCODE_USER_ID - 用户ID (默认: 1)
 */

import { readFileSync, writeFileSync } from "fs";

const mappingFile = process.argv[2];
const csvFile = process.argv[3];
if (!mappingFile || !csvFile) {
  console.error("用法: node match_key_usage.mjs <mapping_file> <csv_file>");
  process.exit(1);
}

const API_URL = process.env.POLARCODE_URL || "https://www.punkcode.cc";
const TOKEN = process.env.POLARCODE_TOKEN;
const USER_ID = process.env.POLARCODE_USER_ID || "1";
if (!TOKEN) {
  console.error("请设置环境变量 POLARCODE_TOKEN");
  process.exit(1);
}

// 1. Parse mapping: id -> key
const mappingText = readFileSync(mappingFile, "utf-8");
const idToKey = new Map();
const keyToId = new Map();
for (const line of mappingText.split("\n")) {
  const parts = line.trim().split(/\t+/);
  if (parts.length >= 3 && /^\d+$/.test(parts[0].trim())) {
    const id = parseInt(parts[0].trim());
    const key = parts[2].trim();
    idToKey.set(id, key);
    // key may map to multiple IDs, store all
    if (!keyToId.has(key)) keyToId.set(key, []);
    keyToId.get(key).push(id);
  }
}
console.log(`映射文件: ${idToKey.size} 个渠道ID`);

// 2. Fetch channel usage from API
async function fetchAllChannels() {
  const all = [];
  let page = 1;
  const pageSize = 100;
  while (true) {
    const url = `${API_URL}/api/channel/?tag_mode=false&id_sort=false&sort_by=id&sort_order=asc&p=${page}&page_size=${pageSize}`;
    const res = await fetch(url, {
      headers: {
        Authorization: TOKEN,
        "New-Api-User": USER_ID,
        "Content-Type": "application/json",
      },
    });
    const json = await res.json();
    if (!json.success) {
      console.error("API 请求失败:", json.message);
      process.exit(1);
    }
    const batch = json.data?.items || json.data?.data || [];
    if (batch.length === 0) break;
    all.push(...batch);
    if (batch.length < pageSize) break;
    page++;
  }
  return all;
}

const channels = await fetchAllChannels();
console.log(`API 返回: ${channels.length} 个渠道`);

// Build id -> used_quota map
const idToUsage = new Map();
for (const ch of channels) {
  idToUsage.set(ch.id, {
    used_quota: ch.used_quota || 0,
    used_usd: (ch.used_quota || 0) / 500000,
    name: ch.name,
  });
}

// 3. Read CSV, match key -> id -> usage, write back
const csvText = readFileSync(csvFile, "utf-8");
const lines = csvText.split("\n");

// Auto-detect separator (tab or comma) from header line
const sep = lines[0] && lines[0].includes("\t") ? "\t" : ",";
console.log(`CSV 分隔符: ${sep === "\t" ? "TAB" : "逗号"}`);

// CSV header is line 0: Key,余额,实际消耗,...
const outputLines = [];
for (let i = 0; i < lines.length; i++) {
  const line = lines[i];
  if (i === 0) {
    outputLines.push(line);
    continue;
  }

  const cols = line.split(sep);
  const csvKey = (cols[0] || "").trim();

  if (!csvKey || !csvKey.startsWith("sk-")) {
    outputLines.push(line);
    continue;
  }

  // Find matching channel IDs for this key
  const matchedIds = keyToId.get(csvKey);
  if (!matchedIds || matchedIds.length === 0) {
    outputLines.push(line);
    continue;
  }

  // Sum usage across all channel IDs with this key
  let totalUsedUsd = 0;
  for (const id of matchedIds) {
    const usage = idToUsage.get(id);
    if (usage) {
      totalUsedUsd += usage.used_usd;
    }
  }

  // Write to column index 2 (实际消耗)
  while (cols.length < 3) cols.push("");
  cols[2] = totalUsedUsd.toFixed(2);

  outputLines.push(cols.join(sep));
}

writeFileSync(csvFile, outputLines.join("\n"), "utf-8");
console.log(`\nCSV 已更新: ${csvFile}`);

// Summary
let totalBalance = 0;
let totalUsed = 0;
let matched = 0;
for (const line of outputLines.slice(1)) {
  const cols = line.split(",");
  const key = (cols[0] || "").trim();
  if (!key.startsWith("sk-")) continue;
  const balance = parseFloat(cols[1]) || 0;
  const used = parseFloat(cols[2]) || 0;
  totalBalance += balance;
  totalUsed += used;
  if (used > 0) matched++;
}
console.log(`匹配到 ${matched} 个key的消耗数据`);
console.log(`总余额: $${totalBalance.toFixed(2)}`);
console.log(`总已用: $${totalUsed.toFixed(2)}`);
