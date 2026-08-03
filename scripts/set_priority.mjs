#!/usr/bin/env node
/**
 * 批量设置指定分组下已启用渠道的优先级（按 ID 顺序从 1000 往下递减）
 *
 * 用法:
 *   node set_priority.mjs <group_name> [start_priority] [batch_size]
 *
 * 环境变量:
 *   POLARCODE_URL   - 服务地址 (默认: https://www.punkcode.cc)
 *   POLARCODE_TOKEN - 管理员 Access Token (必填)
 *   POLARCODE_USER_ID - 用户ID (默认: 1)
 *
 * 示例:
 *   export POLARCODE_TOKEN="your-access-token"
 *   node set_priority.mjs Anthropic              # 从 1000 开始, 每个渠道递减 1
 *   node set_priority.mjs Anthropic 500          # 从 500 开始
 *   node set_priority.mjs Anthropic 1000 10      # 从 1000 开始, 每 10 个共享同一优先级
 */

const group = process.argv[2];
const startPriority = parseInt(process.argv[3] || "1000", 10);
const batchSize = Math.max(1, parseInt(process.argv[4] || "1", 10));

if (!group) {
  console.error("用法: node set_priority.mjs <group_name> [start_priority] [batch_size]");
  process.exit(1);
}

const API_URL = process.env.POLARCODE_URL || "https://www.punkcode.cc";
const TOKEN = process.env.POLARCODE_TOKEN;
const USER_ID = process.env.POLARCODE_USER_ID || "1";

if (!TOKEN) {
  console.error("请设置环境变量 POLARCODE_TOKEN");
  process.exit(1);
}

const headers = {
  Authorization: TOKEN,
  "New-Api-User": USER_ID,
  "Content-Type": "application/json",
};

async function fetchAllChannels() {
  const all = [];
  let page = 1;
  const pageSize = 100;

  while (true) {
    const url = `${API_URL}/api/channel/?tag_mode=false&id_sort=false&sort_by=id&sort_order=asc&p=${page}&page_size=${pageSize}`;
    const res = await fetch(url, { headers });
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

async function updateChannelPriority(channel, priority) {
  const url = `${API_URL}/api/channel/`;
  const res = await fetch(url, {
    method: "PUT",
    headers,
    body: JSON.stringify({
      ...channel,
      priority,
    }),
  });
  const json = await res.json();
  if (!json.success) {
    throw new Error(`更新渠道 ${channel.id} 失败: ${json.message}`);
  }
  return json;
}

async function main() {
  console.log(`分组: ${group}`);
  console.log(`起始优先级: ${startPriority}`);
  console.log(`批次大小: ${batchSize} (每 ${batchSize} 个渠道共享同一优先级)`);
  console.log(`服务地址: ${API_URL}\n`);

  const channels = await fetchAllChannels();
  console.log(`API 返回: ${channels.length} 个渠道`);

  // 筛选: 已启用 (status=1) 且属于指定分组，排除优先级为 1001 的渠道
  const matched = channels.filter((ch) => {
    if (ch.status !== 1) return false;
    if (ch.priority === 1001) return false;
    const groups = (ch.groups || ch.group || "")
      .split(",")
      .map((g) => g.trim())
      .filter(Boolean);
    return groups.includes(group);
  });

  // 按 ID 降序排列（大 ID 优先级更高）
  matched.sort((a, b) => b.id - a.id);

  if (matched.length === 0) {
    console.log(`\n未找到分组 '${group}' 下的已启用渠道`);
    return;
  }

  console.log(`\n匹配到 ${matched.length} 个已启用渠道，开始设置优先级:\n`);

  const headerLine = [
    "ID".padEnd(6),
    "渠道名称".padEnd(26),
    "原优先级".padStart(10),
    "→".padStart(2),
    "新优先级".padStart(10),
    "状态".padStart(6),
  ].join(" ");
  console.log(headerLine);
  console.log("-".repeat(70));

  let success = 0;
  let failed = 0;

  for (let i = 0; i < matched.length; i++) {
    const ch = matched[i];
    const newPriority = startPriority - Math.floor(i / batchSize);
    const oldPriority = ch.priority || 0;

    try {
      await updateChannelPriority(ch, newPriority);
      success++;
      console.log(
        [
          String(ch.id).padEnd(6),
          (ch.name || "unknown").slice(0, 26).padEnd(26),
          String(oldPriority).padStart(10),
          "→".padStart(2),
          String(newPriority).padStart(10),
          "OK".padStart(6),
        ].join(" ")
      );
    } catch (err) {
      failed++;
      console.log(
        [
          String(ch.id).padEnd(6),
          (ch.name || "unknown").slice(0, 26).padEnd(26),
          String(oldPriority).padStart(10),
          "→".padStart(2),
          String(newPriority).padStart(10),
          "FAIL".padStart(6),
        ].join(" ")
      );
      console.error(`  错误: ${err.message}`);
    }
  }

  console.log("-".repeat(70));
  console.log(`\n完成: ${success} 成功, ${failed} 失败`);
  const lowestPriority = startPriority - Math.floor((matched.length - 1) / batchSize);
  console.log(`优先级范围: ${startPriority} → ${lowestPriority}`);
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
