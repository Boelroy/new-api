#!/usr/bin/env node
/**
 * 直接从 API 按 key 字段匹配，更新 pip_key.csv 的"实际使用量"列
 *
 * 用法:
 *   node update_pip_key_usage.mjs <csv_file>
 *
 * 环境变量:
 *   POLARCODE_URL   - 服务地址 (默认: https://www.punkcode.cc)
 *   POLARCODE_TOKEN - 管理员 Access Token (必填)
 *   POLARCODE_USER_ID - 用户ID (默认: 1)
 */

import { readFileSync, writeFileSync } from "fs";

const csvFile = process.argv[2];
if (!csvFile) {
  console.error("用法: node update_pip_key_usage.mjs <csv_file>");
  process.exit(1);
}

const API_URL = process.env.POLARCODE_URL || "https://www.punkcode.cc";
const TOKEN = process.env.POLARCODE_TOKEN;
const USER_ID = process.env.POLARCODE_USER_ID || "1";
if (!TOKEN) {
  console.error("请设置环境变量 POLARCODE_TOKEN");
  process.exit(1);
}

// Fetch all channels from API
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

// Build key -> total used_usd map (sum across all channels with same key)
const keyToUsage = new Map();
for (const ch of channels) {
  const key = (ch.key || "").trim();
  if (!key) continue;
  const usd = (ch.used_quota || 0) / 500000;
  keyToUsage.set(key, (keyToUsage.get(key) || 0) + usd);
}
console.log(`key→usage 映射: ${keyToUsage.size} 个唯一key`);

// Read and update CSV
const csvText = readFileSync(csvFile, "utf-8");
const lines = csvText.split("\n");

const outputLines = [];
let matched = 0;

for (let i = 0; i < lines.length; i++) {
  const line = lines[i];

  // header row
  if (i === 0) {
    outputLines.push(line);
    continue;
  }

  const cols = line.split(",");
  const csvKey = (cols[0] || "").trim();

  if (!csvKey || !csvKey.startsWith("sk-")) {
    outputLines.push(line);
    continue;
  }

  const usd = keyToUsage.get(csvKey);
  if (usd === undefined) {
    console.log(`  未匹配: ${csvKey.slice(0, 30)}...`);
    outputLines.push(line);
    continue;
  }

  while (cols.length < 3) cols.push("");
  cols[2] = usd.toFixed(2);
  outputLines.push(cols.join(","));
  matched++;
  console.log(`  匹配: ${csvKey.slice(0, 30)}... => $${usd.toFixed(2)}`);
}

writeFileSync(csvFile, outputLines.join("\n"), "utf-8");
console.log(`\nCSV 已更新: ${csvFile}`);
console.log(`匹配到 ${matched} 个key的消耗数据`);
