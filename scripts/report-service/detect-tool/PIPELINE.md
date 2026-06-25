# Claude 供应商判别 Pipeline

> 给定一个对外宣称兼容 Claude / Anthropic Messages API 的 endpoint，识别它**真实的底层供应商**。
>
> **本文档是一份 agent runbook**——主要由 Claude Code（或同类 LLM agent）按步骤执行，最终输出双层判别报告（router 层 + backend 层）。**人**通常只在维护规则、debug、审计时才直接读 / 改这份文档。
>
> 基于 2026-05 实测 4 个生产 endpoint（service-inference.ai、tokenutopia.ai、api.nexrouter.ai、Anthropic 1P）总结。

---

## 0. 标准工作流（Claude Code 视角）

用户场景：**"判别这个 endpoint 的真实供应商"** / **"跑一下 provider-detection"** / **"运行 PIPELINE.md"**。

**完整工作流是 §0.1 → §0.2 → §0.3 四步连贯执行**——agent 不应停在 §0.2（只产出 trace）就交差。"PIPELINE 跑完"的标准是：`reports/` 目录下同时存在配对的 `*-trace.md` 和 `*-detection.md`。

### 0.1 凭据准备

`tools/provider-detection/.env` 是凭据约定文件（已 gitignored）。用户填好后 agent 直接读，**不在对话里传 key**。

```bash
# 用户操作（一次性）
cp tools/provider-detection/.env.example tools/provider-detection/.env
# 编辑 .env 填入 URL / KEY / MODEL
```

如果 `.env` 不存在或缺字段，agent 应**主动询问用户**这三个值，并提示用户写进 `.env` 而不是粘贴到对话。`MODEL` 可以暂留空——agent 跑 Step 0（GET /v1/models）拿到候选列表后让用户挑一个再回填。

### 0.2 采集 trace

```bash
node tools/provider-detection/probe.mjs
```

`probe.mjs` 自动读同目录 `.env`，发 6 个 probe（详见 §3），自动写入 `tools/provider-detection/reports/<host>-<model>-<YYYY-MM-DD>-trace.md`（路径可用 `--out` 覆盖，`--out -` 走 stdout）。每条 probe 自带 **Intent** 字段说明它在采什么信号。

### 0.3 应用 §2 信号面板分析 + 输出报告

Agent 读 trace 文件，按 **Tier 1 优先**原则匹配信号：

1. **看到 `usage.inference_geo` 非 null** → 直接定 backend = Anthropic 1P（A2 决定性，跳过其他）
2. **看到 `tool_use[].id` 含 `bdrk_` / `vrtx_` 中缀** → 直接定 backend（A1 决定性）
3. **Tier 1 都没有** → 综合 Tier 2（id 前缀 / SSE 形态 / 错误 envelope / HTTP headers）给 medium confidence
4. **所有 probe 全 4xx** → 输出"凭据/权限不足"，建议用户检查 `.env`，**而非** "unknown"

#### 报告保存位置与文件名

报告**必须落盘**，跟 trace 同目录、共享前缀：

```
tools/provider-detection/reports/<host>-<model>-<YYYY-MM-DD>-trace.md       (probe.mjs 产出)
tools/provider-detection/reports/<host>-<model>-<YYYY-MM-DD>-detection.md   (LLM 判别报告)
```

文件名规则（probe.mjs 自动派生 trace 时遵循同样规则）：

- `<host>`：URL 主域，去掉 `api.` / `www.` 前缀，点替换为 dash
- `<model>`：trace 文件 header 里的 Model 字段，原样使用（已经过 `[^a-zA-Z0-9.-]` → `-` 清洗）
- `<YYYY-MM-DD>`：trace 文件 `Started at` 字段的日期部分

整个 `reports/` 目录**不提交到仓库**（`.gitignore` 已配置）。同一 endpoint 多次判别靠日期区分；同一天多次跑会**覆盖**当天文件。

#### 报告结构

```markdown
# Provider Detection Report: <endpoint>

- URL / Model / Trace 文件路径 + 时间戳

## 结论

| 层      | 标签                                           | Confidence      |
| ------- | ---------------------------------------------- | --------------- |
| Router  | <e.g. new-api fork v1.0.0-rc.1>                | high/medium/low |
| Backend | <e.g. Anthropic 1P / AWS Bedrock / GCP Vertex> | high/medium/low |

## 命中信号

（每条引用 §2 编号：A1 / A2 / A3 / A4 / B1 / B2 / ... / B6）

## 反信号 / 矛盾点

（如果有的话）

## 不确定的部分

（如果信号不够，建议补什么 probe）
```

报告短 — 通常 30-60 行。判别清晰时（A1/A2 命中）甚至可以更短。

---

> 下文是**支撑这套工作流的知识体系**：§1 背景，§2 信号面板（agent 推理依据），§3 各步 probe 的设计意图，§4 决策树，§5 边界 case。维护文档时主要改 §2 的信号表。

---

## 1. 背景：Claude 流量的常见架构

### 1.1 链路深度

实际部署常见 1～3 层：

```
[client]                              ← 你的应用 / proxy
   │
   ▼
[router 层]                           ← OpenRouter / new-api fork / 自研聚合代理
   │（可能再加一层）
   ▼
[中间转售层]（可选）                    ← service-inference 这种"OpenRouter 转售"案例
   │
   ▼
[backend 层]                          ← AWS Bedrock / GCP Vertex / Anthropic 1P
   │
   ▼
[Anthropic Claude 模型权重]
```

### 1.2 两层判别模型

判别问题分两层独立回答，**这份 pipeline 同时输出两层结果**：

| 层             | 问"谁"                       | 例子                                            |
| -------------- | ---------------------------- | ----------------------------------------------- |
| **router 层**  | 你客户端直连的是哪家中间服务 | OpenRouter / new-api / service-inference / 直连 |
| **backend 层** | 真正跑模型的部署平台         | AWS Bedrock / GCP Vertex / Anthropic 1P         |

router 层和 backend 层的判别**信号不同、可信度不同**：

- backend 层判别靠**协议级字段**（id 前缀、inference_geo），路由器难伪造
- router 层判别靠**实现级字段**（错误 envelope、自加 header、流式终止符），是路由器自己的痕迹

---

## 2. 信号面板

按可信度分两 Tier。

### 2.1 Tier 1（决定性信号，看到一条即可定 backend 层）

| #      | 信号                                  | 出现位置                         | 解读                                                                                                                                                                                                       |
| ------ | ------------------------------------- | -------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **A1** | `tool_use[].id` 前缀                  | response body `content[]` 数组里 | `toolu_bdrk_*` → Bedrock；`toolu_vrtx_*` → Vertex；`toolu_01...`（无平台中缀）→ Anthropic 1P                                                                                                               |
| **A2** | `usage.inference_geo` 字段            | response body `usage`            | 非 null（`"global"` 或 `"us"`）→ **Anthropic 1P 直连**。Bedrock / Vertex 协议上不返回这个字段                                                                                                              |
| **A3** | `amazon-bedrock-invocationMetrics`    | SSE `message_stop` event 内      | **非对称信号**：字段**存在** → **AWS Bedrock 直接透传流式 metrics**（决定性）；字段**不存在** ≠ 排除 Bedrock — 部分 router（如 new-api fork）会在转发时剥掉。**单独缺失不能反驳 Bedrock**，靠 A1 / A4 兜底 |
| **A4** | `id` 前缀 `msg_bdrk_*` / `msg_vrtx_*` | response body `id`               | Bedrock / Vertex 直接产出。**前提是路由器没改写 id**（service-inference 这种会改）                                                                                                                         |

**A2 `inference_geo` 协议依据**：Anthropic 官方文档（`platform.claude.com/docs/en/build-with-claude/data-residency`）声明 `inference_geo` **仅在 Claude API (1P) 上返回**——Bedrock / Vertex 协议不返回此字段。某些路由器会在 schema 里塞 `inference_geo: null` 占位，但只有 Anthropic 1P 会返实际值（`"global"` / `"us"`）。

### 2.2 Tier 2（辅助信号，主要用于 router 层判别）

| #      | 信号                    | 出现位置                                  | 解读                                                                                                       |
| ------ | ----------------------- | ----------------------------------------- | ---------------------------------------------------------------------------------------------------------- |
| **B1** | 顶层 `provider` 字段    | response body 顶层                        | router 自报。常见值：`"Amazon Bedrock"`、`"Google"`。OpenRouter / service-inference 系会加，new-api 系不加 |
| **B2** | SSE `id` 前缀 `gen-`    | streaming `message_start` event 里的 `id` | OpenRouter 系流式 id 命名空间。**跟 backend 无关**——只表明 router 是 OpenRouter 系                         |
| **B3** | SSE 末尾 `data: [DONE]` | streaming 流末                            | **OpenRouter / OpenAI-style 兼容层**。Anthropic 直连协议**不发这一行**（标准结尾是 `event: message_stop`） |
| **B4** | 错误 envelope 形态      | 4xx/5xx response body                     | 见下表                                                                                                     |
| **B5** | 错误文案 fingerprint    | 错误 message 字段                         | 见下表                                                                                                     |
| **B6** | HTTP 响应头             | response headers                          | 见下表                                                                                                     |

#### B4：错误 envelope 形态

| 形态                                                                                                                                                               | router                                                                                                      |
| ------------------------------------------------------------------------------------------------------------------------------------------------------------------ | ----------------------------------------------------------------------------------------------------------- |
| `{"error":{"type":"<anthropic_type>","message":"..."}}`（如 `invalid_request_error` / `not_found_error` / `overloaded_error`）                                     | **Anthropic 直连**或纯透传代理                                                                              |
| `{"error":{"code":"ERR_PROVIDER_NNN","type":"server_error","upstream_code":NNN,"param":null},"request_id":"..."}`                                                  | **service-inference 系**（疑似 OpenRouter 套壳）                                                            |
| `{"error":{"code":"<key>","message":"...","type":"new_api_error"}}`                                                                                                | **new-api / one-api fork**（旧版本）                                                                        |
| `{"error":{"type":"<nil>","message":"...(request id: X)(request id: Y)..."}}`：**`type: "<nil>"`（字面字符串）** + **message 末尾 N 个累积的 `(request id: ...)`** | **new-api / one-api fork**（新版本，2026-Q2 起观察到）。多个 request_id 是 new-api 内部多层错误透传链的特征 |
| `{"__type":"ValidationException","message":"..."}` 或 `{"message":"...","__type":"..."}`                                                                           | **AWS Bedrock 直连**                                                                                        |
| `{"error":{"code":"INVALID_ARGUMENT","status":"...","message":"..."}}`                                                                                             | **GCP Vertex 直连**                                                                                         |
| `{"statusCode":500,"error":"...","message":"..."}`                                                                                                                 | Fastify 默认（你自己 proxy 的代码栈）                                                                       |

**辅助 B4 信号**：HTTP 头里 **`x-new-api-version` + `x-oneapi-request-id` 同时出现**直接命中 new-api 系，比错误 envelope 更稳——envelope 形态会跨 new-api 版本变化，header 自报版本号一直存在。

#### B5：错误文案 fingerprint

| 文案片段                                                                      | 来源                                                               |
| ----------------------------------------------------------------------------- | ------------------------------------------------------------------ |
| `"context-compression plugin"`                                                | **OpenRouter**（独有 plugin 名称）                                 |
| `"分组 ccaws 下模型 X 无可用渠道（distributor）"` 等中文                      | **new-api fork**（中国本地化运营）                                 |
| `"预扣费额度失败, 用户剩余额度: ¥..."`                                        | **new-api fork**（计费层中文化）                                   |
| `"InvokeModel: operation error Bedrock Runtime: ..."`（透传 AWS SDK Go 错误） | **Bedrock 直连**或弱透传代理（new-api 有时直接透传，泄露后端身份） |

#### B6：HTTP 响应头 fingerprint

| header                                                     | 来源                                    |
| ---------------------------------------------------------- | --------------------------------------- |
| `x-amzn-RequestId: <uuid>`                                 | **AWS Bedrock 直连**                    |
| `x-cloud-trace-context: ...`                               | **GCP Vertex 直连**                     |
| `request-id: req_...` 或 `anthropic-ratelimit-*-remaining` | **Anthropic 1P 直连**                   |
| `cf-ray: <hex>-<colo>` + `server: cloudflare`              | **CloudFlare 边缘**（router 自己用 CF） |
| `via: 1.1 Caddy`（出现 N 次 = 经过 N 层 Caddy）            | **Caddy 反代**                          |
| `x-new-api-version: <ver>` + `x-oneapi-request-id: ...`    | **new-api / one-api fork**              |
| `openrouter-provider: ...`                                 | **OpenRouter 直连**                     |

经过 router 后，原始 backend 的 trace 头**几乎一定被剥**——所以 `x-amzn-RequestId` 等只在你直连 backend 时才能看到。

### 2.3 反信号（容易误判，看到不要下结论）

下面这些字段**在协议上多个 backend 都会返回**，单条不能区分供应商：

| 字段                                                       | 状态                                                               |
| ---------------------------------------------------------- | ------------------------------------------------------------------ |
| `usage.speed: "standard"`                                  | Anthropic 协议字段，三种 backend 都返回                            |
| `usage.service_tier`                                       | 同上，1P / Bedrock / Vertex 都可能返回 `"standard"` / `"priority"` |
| `usage.cache_creation: {ephemeral_5m..., ephemeral_1h...}` | Sonnet 4.5+ 协议字段，三种 backend 都支持双 TTL                    |
| `id` 前缀 `chatcmpl-...` 或 `gen-...`                      | router 重写过的 id，**判别 backend 时无效**，但能看出 router 风格  |
| 字段缺失/为 null（如 `inference_geo: null`）               | 路由器 schema 占位，跟 backend 没关系                              |

---

## 3. 主动探测 Pipeline

`probe.mjs` 按顺序发 6 个 probe，全程 ≤ 7 个请求、~10s wall-clock。每个 Step 的目的对应 §2 的哪些信号：

| Step   | 请求                                           | 主要采集信号（§2 编号）                                             |
| ------ | ---------------------------------------------- | ------------------------------------------------------------------- |
| **0**  | `GET /v1/models`                               | B6（HTTP headers）+ owned_by / supported_endpoint_types             |
| **1**  | Plain probe，`max_tokens=10`                   | A2（inference_geo）+ A4（id 前缀）+ B1（顶层 provider）+ B6         |
| **2**  | Tools probe，`tool_choice` 强制调用            | **A1（tool_use[].id 前缀，决定性）**                                |
| **3**  | Streaming probe                                | A3（amazon-bedrock-invocationMetrics）+ B2（gen- id）+ B3（[DONE]） |
| **4a** | `max_tokens=99999999`（context-overflow 错误） | B5（OpenRouter 独有 `context-compression plugin`）                  |
| **4b** | `role="alien"`（validation 错误）              | B4（错误 envelope 形态）+ B5                                        |

**Step 2 是终极判别**：tool_use id 嵌在 `content[]` 数组里，路由器要伪造得做深度 body 重写——实测没有路由器这么做。如果其他 Step 信号被改写干扰（比如 id 重写成 `chatcmpl-*`），靠 Step 2 兜底。

**Step 4a 的金标准**：错误信息里 `"context-compression plugin"` 字眼是 OpenRouter 独有 plugin 名，被路由器透传出来即可锁死 router 层 = OpenRouter 系。

---

## 4. 决策树

```
1. Step 0 headers 自报？
   ├─ x-amzn-RequestId       → backend = AWS Bedrock 直连（终止）
   ├─ x-new-api-version      → router = new-api（继续判 backend）
   └─ openrouter-* / x-cloud-trace-context → router = OpenRouter / GCP（继续判 backend）

2. Step 1 看 usage.inference_geo
   └─ 非 null                 → backend = Anthropic 1P（A2 终止）

3. Step 2 看 tool_use[].id 前缀
   ├─ toolu_bdrk_*           → backend = AWS Bedrock（A1）
   ├─ toolu_vrtx_*           → backend = GCP Vertex（A1）
   └─ toolu_01...            → backend = Anthropic 1P（A1）

4. Step 2 拿不到 tool_use（罕见）
   └─ 退到 Step 3（A3 / B2 / B3）+ Step 4（B4 / B5）综合 Tier 2 判断

输出：router 层 + backend 层 双标签 + confidence
```

---

## 5. 边界 Case

### 5.1 模型不调 tool，Tools Probe 失效

probe.mjs 已内置 `tool_choice: {"type":"tool","name":"get_weather"}` 强制调用。如果仍然拿不到 tool_use（路由器不传 tool_choice，或模型拒绝），靠 `usage.inference_geo` + 流式 `amazon-bedrock-invocationMetrics` 兜底。

### 5.2 Endpoint 大量 5xx 噪声

实测 service-inference.ai 的 Anthropic 1P 路径（dash-style model 名）小流量都被 503 限流。配额受限本身是个**间接信号**——OpenRouter / Bedrock 路径池子大很少 503，1P 直连小客户配额紧。

但**判别期间不要硬刷**：5xx 期间所有信号都拿不到，靠不密集的少量探测样本（Step 1-4 各 1 次）观察形态即可。

### 5.3 路由器主动剥字段

router 可以剥掉响应里的 `x-amzn-RequestId` / `id` 改写成 `chatcmpl-*`。但**剥不掉**：

- `tool_use[].id` 嵌套深，重写成本高
- `inference_geo` 是 Anthropic 1P 协议字段，路由器主动注入"global"反而是诚实标识

实测 service-inference.ai 改写顶层 id 但保留 `tool_use[].id`，这是 router 通用模式。

### 5.4 同一 endpoint，不同 model 名走不同 backend

实测 service-inference.ai：

- `claude-sonnet-4.6`（点分）→ OpenRouter→Bedrock/Vertex
- `claude-sonnet-4-6`（dash 分）→ Anthropic 1P 直连

**不要假设一个 endpoint 单一后端**。脚本应该对每个 model 名独立判别。

---

## 附录 A: probe.mjs 实现细节

§0 已经描述了 agent 工作流。这里只补充脚本层面的实现细节，给**维护脚本**的人参考。

### 输入约定

按优先级：CLI flags（`--url X --key Y --model Z --out PATH --timeout N`）> 环境变量（`URL` / `KEY` / `MODEL`）> `.env` 文件（脚本同目录，自动加载）。

任一来源缺 url/key/model 就退到 `--help` 报错。`--timeout` 默认 30000ms（30s），每个 probe 独立计时，超时后输出 `<timeout after Nms>` 而非挂起。

### 输出格式

单个 Markdown 文件：

```
# Provider Detection Trace Bundle
- URL / Model / Started at / 探针数

## Step 0: GET /v1/models
**Intent**: <这一步采集的信号清单>
**HTTP**: <status> (<elapsed>ms)
### Headers
<完整 response headers>
### Body
<完整 response body，JSON 自动 pretty-print>

## Step 1: ... ## Step 4b: ...
```

**JSON body 自动 pretty-print**：当 `content-type: application/json` 时，body 经 `JSON.parse` + `JSON.stringify(_, _, 2)` 重新序列化，避免 `\uXXXX` 这类 unicode escape 干扰阅读（典型例子：new-api 错误体里的 `<nil>` 字符串）。SSE / HTML / 其他形态保持原样。
