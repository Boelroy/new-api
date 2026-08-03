#!/usr/bin/env node
/**
 * 汇总指定日期（上海时间 00:00 ~ 24:00）当天创建的渠道的实际累计用量。
 * 数据源: /api/channel/ 直接读取 channels.used_quota, 不遍历日志明细。
 *
 * 用法:
 *   node get_channel_usage_by_date.mjs <YYYY-MM-DD>
 *
 * 环境变量:
 *   NEWAPI_URL         - 服务地址 (必填, 形如 https://api.example.com)
 *   NEWAPI_TOKEN       - Admin/Root Access Token (必填, 需要 admin 才能访问 /api/channel/)
 *   NEWAPI_USER_ID     - 该 Token 对应的用户 ID (必填)
 *   NEWAPI_CONCURRENCY - 渠道分页并发数 (默认 8)
 *
 * 示例:
 *   export NEWAPI_URL="https://api.example.com"
 *   export NEWAPI_TOKEN="..."
 *   export NEWAPI_USER_ID="1"
 *   node get_channel_usage_by_date.mjs 2026-07-14
 */

const args = process.argv.slice(2);
const dateArg = args.find((a) => /^\d{4}-\d{2}-\d{2}$/.test(a));
if (!dateArg) {
  console.error("用法: node get_channel_usage_by_date.mjs <YYYY-MM-DD>");
  process.exit(1);
}

const API_URL = (process.env.NEWAPI_URL || "").replace(/\/+$/, "");
const TOKEN = process.env.NEWAPI_TOKEN;
const USER_ID = process.env.NEWAPI_USER_ID;
const CONCURRENCY = Number(process.env.NEWAPI_CONCURRENCY || 8);

if (!API_URL) {
  console.error("请设置环境变量 NEWAPI_URL (形如 https://api.example.com)");
  process.exit(1);
}
if (!TOKEN) {
  console.error("请设置环境变量 NEWAPI_TOKEN");
  process.exit(1);
}
if (!USER_ID) {
  console.error("请设置环境变量 NEWAPI_USER_ID (Token 对应的用户 ID)");
  process.exit(1);
}

const PAGE_SIZE = 100; // newapi 单页上限
const QUOTA_PER_USD = 500_000;

// 上海时间恒为 UTC+8, 无夏令时。
const startTs = Math.floor(
  new Date(`${dateArg}T00:00:00+08:00`).getTime() / 1000
);
const endTs = startTs + 86400; // 下一天 00:00, exclusive

const commonHeaders = {
  Authorization: TOKEN,
  "New-Api-User": USER_ID,
  "Content-Type": "application/json",
};

async function apiGet(path) {
  const res = await fetch(`${API_URL}${path}`, { headers: commonHeaders });
  const text = await res.text();
  let json;
  try {
    json = JSON.parse(text);
  } catch {
    throw new Error(`非 JSON 响应 (${res.status}): ${text.slice(0, 200)}`);
  }
  if (!json.success) {
    throw new Error(`${path} 请求失败: ${json.message || res.status}`);
  }
  return json.data;
}

function channelListPath(page) {
  const params = new URLSearchParams({
    p: String(page),
    page_size: String(PAGE_SIZE),
    id_sort: "true", // 按 id desc, 新建的渠道排在前面
  });
  return `/api/channel/?${params.toString()}`;
}

async function fetchChannelPage(page) {
  const data = await apiGet(channelListPath(page));
  const items = data.items || data.data || [];
  const total = data.total ?? data.page_total ?? 0;
  return { total, items };
}

async function fetchAllChannels() {
  const first = await fetchChannelPage(1);
  const total = first.total;
  const totalPages = Math.max(1, Math.ceil(total / PAGE_SIZE));

  const all = [...first.items];
  if (totalPages > 1) {
    const queue = [];
    for (let p = 2; p <= totalPages; p++) queue.push(p);
    let done = 1;
    const workers = Array.from({ length: CONCURRENCY }, async () => {
      while (queue.length) {
        const p = queue.shift();
        const { items } = await fetchChannelPage(p);
        all.push(...items);
        done++;
        if (done === totalPages || done % 20 === 0) {
          process.stderr.write(`\r抓取渠道分页: ${done}/${totalPages}   `);
        }
      }
    });
    await Promise.all(workers);
    process.stderr.write("\n");
  }

  return { total, channels: all };
}

function fmtLocal(ts, tz = "Asia/Shanghai") {
  if (!ts) return "-";
  const d = new Date(ts * 1000);
  const parts = new Intl.DateTimeFormat("en-CA", {
    timeZone: tz,
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
    hour12: false,
  }).formatToParts(d);
  const g = Object.fromEntries(parts.map((p) => [p.type, p.value]));
  return `${g.year}-${g.month}-${g.day} ${g.hour}:${g.minute}:${g.second}`;
}

function truncate(s, width) {
  const str = String(s ?? "");
  if (str.length <= width) return str.padEnd(width);
  return str.slice(0, Math.max(0, width - 1)) + "…";
}

async function main() {
  console.log(`服务地址: ${API_URL}`);
  console.log(
    `创建时间窗口: ${dateArg} 00:00:00 ~ 23:59:59 上海时间 (epoch ${startTs} ~ ${
      endTs - 1
    })`
  );

  const { total, channels } = await fetchAllChannels();
  console.log(`拉取渠道总数: ${channels.length}/${total}\n`);

  const matched = channels
    .filter((c) => {
      const t = Number(c.created_time || 0);
      return t >= startTs && t < endTs;
    })
    .sort((a, b) => (b.used_quota || 0) - (a.used_quota || 0));

  if (matched.length === 0) {
    console.log(`${dateArg} 当天没有新建渠道`);
    return;
  }

  const nameCol = 30;
  const header = [
    "ID".padEnd(8),
    "Name".padEnd(nameCol),
    "Created (Shanghai)".padEnd(20),
    "Group".padEnd(12),
    "Status".padStart(6),
    "Used Quota".padStart(14),
    "Cost (USD)".padStart(14),
  ].join(" ");
  const ruleWidth = header.length;
  console.log(header);
  console.log("-".repeat(ruleWidth));

  let totalUsed = 0;
  for (const c of matched) {
    const used = Number(c.used_quota || 0);
    totalUsed += used;
    console.log(
      [
        String(c.id).padEnd(8),
        truncate(c.name || "", nameCol),
        fmtLocal(c.created_time).padEnd(20),
        truncate(c.group || "", 12),
        String(c.status ?? "").padStart(6),
        used.toLocaleString().padStart(14),
        (used / QUOTA_PER_USD).toFixed(6).padStart(14),
      ].join(" ")
    );
  }

  console.log("-".repeat(ruleWidth));
  console.log(
    [
      "TOTAL".padEnd(8),
      "".padEnd(nameCol),
      "".padEnd(20),
      "".padEnd(12),
      "".padStart(6),
      totalUsed.toLocaleString().padStart(14),
      (totalUsed / QUOTA_PER_USD).toFixed(6).padStart(14),
    ].join(" ")
  );

  console.log("");
  console.log(`匹配渠道数: ${matched.length}`);
  console.log(`累计消费: $${(totalUsed / QUOTA_PER_USD).toFixed(6)}`);
}

main().catch((err) => {
  console.error("\n出错:", err.message || err);
  process.exit(1);
});
