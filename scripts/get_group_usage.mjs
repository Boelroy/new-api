#!/usr/bin/env node
/**
 * 获取指定分组下已启用渠道的已用额度
 *
 * 用法:
 *   node get_group_usage.mjs <group_name>
 *
 * 环境变量:
 *   POLARCODE_URL   - 服务地址 (默认: https://polarcode.sxsxsxsx.uk)
 *   POLARCODE_TOKEN - 管理员 Access Token (必填)
 *
 * 示例:
 *   export POLARCODE_TOKEN="your-access-token"
 *   node get_group_usage.mjs default
 *   node get_group_usage.mjs vip
 */

const group = process.argv[2];
if (!group) {
  console.error("用法: node get_group_usage.mjs <group_name>");
  process.exit(1);
}

const API_URL = process.env.POLARCODE_URL || "https://www.punkcode.cc";
const TOKEN = process.env.POLARCODE_TOKEN;
const USER_ID = process.env.POLARCODE_USER_ID || "1";
if (!TOKEN) {
  console.error("请设置环境变量 POLARCODE_TOKEN");
  process.exit(1);
}

async function fetchChannels() {
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

async function main() {
  console.log(`查询分组: ${group}`);
  console.log(`服务地址: ${API_URL}\n`);

  const channels = await fetchChannels();

  const matched = channels.filter((ch) => {
    const groups = (ch.groups || ch.group || "")
      .split(",")
      .map((g) => g.trim())
      .filter(Boolean);
    return groups.includes(group);
  });

  if (matched.length === 0) {
    console.log(`未找到分组 '${group}' 下的已启用渠道`);
    return;
  }

  console.log(`分组 '${group}' 下共 ${matched.length} 个已启用渠道:\n`);

  const header = [
    "ID".padEnd(6),
    "渠道名称".padEnd(26),
    "已用额度".padStart(12),
    "已用(USD)".padStart(12),
    "余额(USD)".padStart(12),
  ].join(" ");
  console.log(header);
  console.log("-".repeat(78));

  let totalUsed = 0;

  for (const ch of matched) {
    const used = ch.used_quota || 0;
    const usd = used / 500000;
    const balance = ch.balance || 0;
    totalUsed += used;

    console.log(
      [
        String(ch.id).padEnd(6),
        (ch.name || "unknown").slice(0, 26).padEnd(26),
        used.toLocaleString().padStart(12),
        usd.toFixed(4).padStart(12),
        balance.toFixed(4).padStart(12),
      ].join(" ")
    );
  }

  console.log("-".repeat(78));
  console.log(
    [
      "合计".padEnd(6),
      "".padEnd(26),
      totalUsed.toLocaleString().padStart(12),
      (totalUsed / 500000).toFixed(4).padStart(12),
    ].join(" ")
  );
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
