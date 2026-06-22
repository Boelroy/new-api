# Daily Gross Profit Reporting — Implementation Plan

> 在现有 `scripts/report-service` 内新增「每日毛利」面板，跨 System 1（主）+ System 2（pipi）汇总。

## 1. 背景

- **System 1**：主 newapi，54.178.16.161
  - 下游渠道（客户）通过不同 token group 接入：`zl-anthropic-a` / `zl-anthropic-z` / `zl-anthropic-x`
  - 上游渠道分别打到不同 key 上：maas official / maas-aws-z / maas-aws-x
  - 特殊渠道：**channel id 1489** 是一个大 key，实际转发到 System 2
- **System 2 (pipi)**：第二套 newapi，ec2-16-76-159-22.ap-northeast-1
  - 包含每个子 key 的真实用量
  - 已部署 report-service

## 2. 毛利公式

```
upstream_cost_cny   = used_usd × upstream_unit_price_cny   (per-key)
downstream_rev_cny  = used_usd × downstream_unit_price_cny (per-group)
profit_usd          = (downstream_rev_cny - upstream_cost_cny) / 7
```

- `7` = 美金汇率（CNY/USD），先硬编码
- **上游单价按 key（channel_id）维度**，每个 key 单价不同
- **下游单价按 token group**，例如 `zl-anthropic-a=4.5`

## 3. 关键决策

| 决策点              | 选择                                                                                   |
| ------------------- | -------------------------------------------------------------------------------------- |
| 下游售价配置粒度    | 按 token group（利用现有 newapi group 机制，UI 可配）                                  |
| System 2 数据接入   | HTTP API + X-API-Key（System 2 已部署 report-service）                                 |
| 上游单价配置粒度    | **按 key（channel_id）** — 扩展 `report_key_quotas` 表加 `unit_price_cny` 列            |
| `channels.tag` 作用 | 仅作为 (1) pipi 标记 (`tag='pipi'`)、(2) UI 分组展示                                    |

## 4. 数据模型

### 4.1 扩展现有表

```sql
ALTER TABLE report_key_quotas ADD COLUMN IF NOT EXISTS unit_price_cny NUMERIC(8,4);
ALTER TABLE report_key_quotas ADD COLUMN IF NOT EXISTS note TEXT NOT NULL DEFAULT '';
```

### 4.2 新增表

```sql
-- 下游售价（按 token group）
CREATE TABLE IF NOT EXISTS report_downstream_pricing (
    "group"         TEXT PRIMARY KEY,
    unit_price_cny  NUMERIC(8,4) NOT NULL,
    note            TEXT NOT NULL DEFAULT '',
    updated_at      BIGINT NOT NULL
);

-- Pipi 每日同步（每行带单价快照，便于后期回算）
CREATE TABLE IF NOT EXISTS report_pipi_daily (
    date              TEXT NOT NULL,
    channel_id        BIGINT NOT NULL,   -- System 2 的 channel id
    channel_name      TEXT NOT NULL DEFAULT '',
    channel_tag       TEXT NOT NULL DEFAULT '',
    request_count     INT NOT NULL DEFAULT 0,
    total_cost_usd    NUMERIC(14,6) NOT NULL DEFAULT 0,
    unit_price_cny    NUMERIC(8,4),
    updated_at        BIGINT NOT NULL,
    PRIMARY KEY (date, channel_id)
);
```

## 5. 实施计划

### Phase 1 — Schema 迁移

- 文件：`scripts/report-service/main.go`
- 在 `main()` 启动时的 DDL 列表中追加上述三段
- 保持向后兼容（`IF NOT EXISTS`、ADD COLUMN IF NOT EXISTS）

### Phase 2 — API Key 认证（双方 report-service）

- 文件：`scripts/report-service/main.go`
- `authMiddleware` 增加 `X-API-Key` 短路通道
- env: `REPORT_API_KEY`
- **部署**：System 2 box 重部署 + 设置 env

```go
if apiKey := c.GetHeader("X-API-Key"); apiKey != "" && reportAPIKey != "" && apiKey == reportAPIKey {
    c.Next()
    return
}
```

### Phase 3 — 扩展 `/api/allkeys/data`

- 文件：`scripts/report-service/main.go`、`frontend/src/api.ts`
- `ChannelRow` / `queryAllKeys` 加 `tag string` + `unit_price_cny *float64`
- 这是 System 1 拉 System 2 的契约，必须先做

### Phase 4 — Pipi Sync 后台任务

- 新文件：`scripts/report-service/pipi.go`
- env: `PIPI_REPORT_URL`, `PIPI_REPORT_API_KEY`
- `startPipiSync()`：每小时 + 启动时跑一次
  - `GET <PIPI>/api/report?start=&end=` → 按 (date, channel_id) sum total_cost
  - `GET <PIPI>/api/allkeys/data` → 拿 tag + unit_price_cny
  - 按 channel_id join，UPSERT 到 `report_pipi_daily`

### Phase 5 — Pricing CRUD 端点

- 文件：`scripts/report-service/main.go`

```
GET  /api/profit/keys/pricing            # list (channel_id, name, tag, used_usd, quota_usd, unit_price_cny, note)
POST /api/profit/keys/pricing            # upsert unit_price_cny + note (复用 report_key_quotas)
GET  /api/profit/downstream/pricing
POST /api/profit/downstream/pricing
```

### Phase 6 — Profit 计算端点

- 新文件：`scripts/report-service/profit.go`
- `GET /api/profit/daily?start=&end=`

三步查询：

**Step 1** — 非 pipi（System 1 直算）：

```sql
SELECT
    LEFT(r.hour,10) AS date,
    r.channel_id,
    COALESCE(c.tag,'') AS channel_tag,
    r."group" AS token_group,
    SUM(r.total_cost) AS used_usd,
    q.unit_price_cny AS up_price
FROM report_daily_agg r
LEFT JOIN channels c ON c.id = r.channel_id
LEFT JOIN report_key_quotas q ON q.channel_id = r.channel_id
WHERE LEFT(r.hour,10) BETWEEN $1 AND $2
  AND COALESCE(c.tag,'') <> 'pipi'
GROUP BY date, r.channel_id, channel_tag, token_group, q.unit_price_cny;
```

**Step 2** — pipi 收入侧：

```sql
SELECT LEFT(r.hour,10) AS date, r."group" AS token_group, SUM(r.total_cost) AS revenue_usd
FROM report_daily_agg r LEFT JOIN channels c ON c.id=r.channel_id
WHERE LEFT(r.hour,10) BETWEEN $1 AND $2 AND COALESCE(c.tag,'')='pipi'
GROUP BY date, token_group;
```

**Step 3** — pipi 成本侧：

```sql
SELECT date, channel_id, channel_tag, total_cost_usd, unit_price_cny
FROM report_pipi_daily
WHERE date BETWEEN $1 AND $2;
```

**Go 端 join 计算**：

- 非 pipi 行：`profit = used_usd × (down_price[group] - up_price) / 7`
- Pipi：`revenue_cny = Σ(revenue_usd × down_price[group])`、`cost_cny = Σ(total_cost_usd × unit_price_cny)`、`profit = (revenue_cny - cost_cny) / 7`
- 缺单价的 row 进 `missing_pricing` 数组返回，UI 高亮

响应：

```json
{
  "start": "2026-06-15",
  "end":   "2026-06-21",
  "daily": [
    {"date":"2026-06-21","revenue_cny":...,"cost_cny":...,"profit_usd":...,"profit_rate":...}
  ],
  "by_key":   [{"channel_id":..., "tag":"maas-aws-z", "used_usd":..., "cost_cny":...}],
  "by_group": [{"group":"zl-anthropic-a", "revenue_usd":..., "revenue_cny":...}],
  "missing_pricing": {"channel_ids":[...], "groups":[...]}
}
```

### Phase 7 — 前端 Profit 页面

- 新文件：`frontend/src/pages/Profit.tsx`
- 修改：`api.ts`（新端点）、`App.tsx`（加 `/profit` 路由）、`components/Sidebar.tsx`（加入口）

布局：

- 日期范围 + 刷新
- Summary cards：总用量 USD / 上游成本 CNY / 下游售价 CNY / **毛利 USD** / 毛利率%
- 双图：每日毛利柱状 + 按 tag 成本占比
- 上游单价编辑表（per key，pipi 同步过来的也展示，来源列标 system1/pipi）
- 下游单价编辑表（per group）
- 缺价告警 banner

### Phase 8 — Bootstrap

- System 1 现有 channel 打 tag：
  - `1489` → `pipi`
  - MAAS 官方 channels → `maas-official`
  - AWS-Z → `maas-aws-z`
  - AWS-X → `maas-aws-x`
- 方式待定：SQL 脚本 vs newapi 后台手动

## 6. 文件清单

```
scripts/report-service/
├── main.go             (M) schema migration, API Key 认证, /api/profit/* handlers, 扩展 ChannelRow/queryAllKeys
├── pipi.go             (NEW) HTTP client + 同步循环
├── profit.go           (NEW) 利润聚合 (Step 1-3 + Go join)
└── frontend/src/
    ├── api.ts                   (M) 新端点 + 类型
    ├── App.tsx                  (M) /profit 路由
    ├── components/Sidebar.tsx   (M) 入口
    └── pages/Profit.tsx         (NEW)
```

## 7. 风险与开放问题

| 等级 | 项                                                                                                                                                 |
| ---- | -------------------------------------------------------------------------------------------------------------------------------------------------- |
| HIGH | System 2 box 需要重部署带 `REPORT_API_KEY` 的版本 — 谁来跑                                                                                          |
| MED  | 现有 channel 大多 `tag IS NULL`，需要补打 tag                                                                                                       |
| MED  | System 1 → System 2 网络连通性（Tokyo 内网 or 公网 HTTPS？SG 是否需要放通）                                                                          |
| MED  | System 1 channel 1489 logs.cost ↔ System 2 实际成本可能有 1-2% 偏差（重试/超时），UI 同时展示两个数字便于发现异常                                    |
| LOW  | 汇率 7 先硬编码，后面再加 config                                                                                                                    |
| LOW  | per-key 单价的运维成本：每开新 key 要补单价；建议 UI 提供批量设置或在 KeyCapacity 页打通编辑入口                                                       |

## 8. 推进顺序

1. System 2 部署带 `REPORT_API_KEY` 的版本
2. System 1 现有 channels 打 tag（bootstrap）
3. Schema + Pricing CRUD + UI 价格配置页 → 部署
4. Pipi sync → 部署，等一轮（1 小时）
5. Profit endpoint + Profit UI → 部署
6. 对账：手算 1 天的毛利 vs UI 显示，验证一致

## 9. 复杂度估算

- 后端：~6h（schema、auth、profit calc、CRUD、tag 扩展、pipi sync）
- 前端：~4h（Profit 页 + 两个 pricing 编辑表）
- Bootstrap + 部署 + 对账：~2h
- **总计：约 10-12 小时**
