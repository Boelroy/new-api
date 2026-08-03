# Claude `/v1/messages` API 完全指南

> 基于 new-api 代码库整理，适用于 Anthropic 直连 和 AWS Bedrock 两种渠道。

---

## 目录

1. [请求结构总览](#1-请求结构总览)
2. [请求头](#2-请求头)
3. [基础采样参数](#3-基础采样参数)
4. [系统消息 system](#4-系统消息-system)
5. [对话消息 messages](#5-对话消息-messages)
6. [多模态内容（图片/文档）](#6-多模态内容图片文档)
7. [工具调用 tools](#7-工具调用-tools)
8. [Tool Choice](#8-tool-choice)
9. [Extended Thinking](#9-extended-thinking)
10. [提示词缓存 cache_control](#10-提示词缓存-cache_control)
11. [Web Search Tool](#11-web-search-tool)
12. [高级字段](#12-高级字段)
13. [响应结构](#13-响应结构)
14. [Token 用量与计费](#14-token-用量与计费)
15. [完整请求示例](#15-完整请求示例)
16. [参数约束速查表](#16-参数约束速查表)
17. [多轮对话 Tool 调用流程图](#17-多轮对话-tool-调用流程图)
18. [模型列表与能力矩阵](#18-模型列表与能力矩阵)

---

## 1. 请求结构总览

```
POST https://api.anthropic.com/v1/messages
```

```
请求头
├── x-api-key          必填  认证密钥
├── anthropic-version  必填  API 版本
└── anthropic-beta     选填  开启测试特性

请求体（JSON）
├── model              必填  模型名称
├── messages           必填  对话历史
├── max_tokens         必填* 最大输出 token
│
├── system             选填  系统提示词
├── stream             选填  是否流式输出
│
├── temperature        选填  随机性
├── top_p              选填  核采样
├── top_k              选填  候选 token 数
├── stop_sequences     选填  停止词
│
├── tools              选填  工具定义列表
├── tool_choice        选填  工具选择策略
│
├── thinking           选填  Extended Thinking 配置
├── output_config      选填  输出控制（effort 等）
├── cache_control      选填  提示词缓存
└── metadata           选填  用户元数据
```

> `*` max_tokens 不填时系统自动补默认值（通常 8192），建议显式指定。

---

## 2. 请求头

| 请求头 | 必填 | 示例值 | 说明 |
|--------|------|--------|------|
| `x-api-key` | 是 | `sk-ant-xxx` | Anthropic API Key |
| `anthropic-version` | 是 | `2023-06-01` | 固定写此值，不填报错 |
| `anthropic-beta` | 否 | 见下表 | 开启测试功能 |

### anthropic-beta 可选值

| 值 | 作用 |
|----|------|
| `interleaved-thinking-2025-05-14` | 允许 thinking 块和 tool_use 块交替出现（Thinking + Tool 同时使用时必须加） |
| `prompt-caching-2024-07-31` | 开启提示词缓存 |
| `max-tokens-3-5-sonnet-2024-07-15` | 解锁 claude-3-5-sonnet 的更高 max_tokens 上限 |

多个 beta 特性用逗号分隔：
```
anthropic-beta: interleaved-thinking-2025-05-14,prompt-caching-2024-07-31
```

---

## 3. 基础采样参数

| 参数 | 类型 | 范围 | 默认 | 说明 |
|------|------|------|------|------|
| `model` | string | — | — | 模型名称（见第 18 节） |
| `max_tokens` | uint | 1 ~ 模型上限 | 8192 | 最大输出 token 数（不含输入） |
| `temperature` | float | [0.0, 1.0] | 1.0 | 随机性；Thinking 模式下强制为 1.0 |
| `top_p` | float | (0.0, 1.0] | — | 核采样概率阈值；Thinking 模式下自动清除 |
| `top_k` | int | ≥ 1 | — | 候选 token 数上限；Thinking 模式下自动清除 |
| `stop_sequences` | []string | 最多 4 个 | — | 遇到时立即停止，不包含在输出中 |
| `stream` | bool | — | false | 是否流式输出（SSE） |

### temperature 使用建议

```
0.0        最确定，适合代码生成、数据提取
0.3~0.7    平衡，适合问答、摘要
1.0        最随机，适合创意写作；Thinking 模式必须使用
```

### temperature 和 top_p 的关系

二者都是控制输出多样性的采样策略，**通常只设一个，不要同时设置**：
- `temperature` — 调整 logit 分布的"温度"
- `top_p` — 截断概率累积到 p 以上的 token 集合

---

## 4. 系统消息 system

### 字符串形式（简单）

```json
"system": "你是一个专业的数据分析助手，请用中文回答。"
```

### 数组形式（支持缓存 / 多模态）

```json
"system": [
  {
    "type": "text",
    "text": "你是一个专业的数据分析助手，请用中文回答。",
    "cache_control": { "type": "ephemeral" }
  },
  {
    "type": "text",
    "text": "以下是公司产品文档：[长篇文档内容...]",
    "cache_control": { "type": "ephemeral" }
  }
]
```

- 数组格式允许对每个 text 块单独打 `cache_control` 标记
- system 消息不会出现在对话历史中，但每次请求都会被模型读到
- 适合放：角色设定、背景知识、固定规则、长篇参考文档

---

## 5. 对话消息 messages

### 基本规则

```
- messages 是一个数组，按时间顺序排列
- role 只有两种值：user / assistant
- 第一条必须是 user 消息
- user 和 assistant 必须严格交替（同角色相邻会被自动合并）
- tool_result 放在 user 消息里
- tool_use 放在 assistant 消息里
```

### 最简单的消息

```json
"messages": [
  { "role": "user", "content": "你好，请介绍一下自己。" },
  { "role": "assistant", "content": "你好！我是 Claude..." },
  { "role": "user", "content": "你能做什么？" }
]
```

### content 的两种形式

**字符串（纯文本）**

```json
{ "role": "user", "content": "这是一段文字" }
```

**数组（多模态/复合内容）**

```json
{
  "role": "user",
  "content": [
    { "type": "text", "text": "第一段文字" },
    { "type": "image", "source": { ... } },
    { "type": "text", "text": "第二段文字" }
  ]
}
```

### content 块的 type 值

| type | 出现在 | 说明 |
|------|--------|------|
| `text` | user / assistant | 纯文本 |
| `image` | user | 图片（base64 或 URL） |
| `document` | user | 文档（PDF 等） |
| `tool_use` | assistant | 模型发起工具调用 |
| `tool_result` | user | 工具执行结果 |
| `thinking` | assistant | 模型思考过程（Thinking 模式） |

---

## 6. 多模态内容（图片/文档）

### 图片 — base64 方式

```json
{
  "type": "image",
  "source": {
    "type": "base64",
    "media_type": "image/png",
    "data": "iVBORw0KGgoAAAANSUhEUgAAAAE..."
  }
}
```

### 图片 — URL 方式

```json
{
  "type": "image",
  "source": {
    "type": "url",
    "url": "https://example.com/image.png"
  }
}
```

> **注意：** AWS Bedrock 不支持 URL 方式，代码会自动 fetch URL 并转换为 base64。

### 支持的图片格式

| media_type | 格式 |
|-----------|------|
| `image/jpeg` | JPEG |
| `image/png` | PNG |
| `image/gif` | GIF |
| `image/webp` | WebP |

### 文档

```json
{
  "type": "document",
  "source": {
    "type": "base64",
    "media_type": "application/pdf",
    "data": "JVBERi0xLjQK..."
  }
}
```

---

## 7. 工具调用 tools

### Tool 定义

```json
"tools": [
  {
    "name": "get_weather",
    "description": "查询指定城市的实时天气",
    "input_schema": {
      "type": "object",
      "properties": {
        "city": {
          "type": "string",
          "description": "城市名称，如 '上海'"
        },
        "unit": {
          "type": "string",
          "enum": ["celsius", "fahrenheit"],
          "description": "温度单位"
        }
      },
      "required": ["city"]
    }
  }
]
```

| 字段 | 必填 | 说明 |
|------|------|------|
| `name` | 是 | 工具名，只能包含字母/数字/下划线 |
| `description` | 推荐 | 描述越详细，模型调用越准确 |
| `input_schema` | 是 | 标准 JSON Schema，描述参数结构 |

### 完整的多轮 Tool 调用流程

```
第 1 轮请求
└── messages: [{ role: user, content: "查询上海天气" }]

第 1 轮响应（stop_reason: tool_use）
└── content: [
      { type: tool_use, id: "toolu_01", name: "get_weather", input: { city: "上海" } }
    ]

第 2 轮请求（把上一轮响应原样加入 messages，再追加 tool_result）
└── messages: [
      { role: user,      content: "查询上海天气" },
      { role: assistant, content: [{ type: tool_use, id: "toolu_01", ... }] },
      { role: user,      content: [{ type: tool_result, tool_use_id: "toolu_01", content: "25°C 晴" }] }
    ]

第 2 轮响应（stop_reason: end_turn）
└── content: [{ type: text, text: "上海现在是 25°C，晴天，适合出行。" }]
```

> **关键：** `tool_result.tool_use_id` 必须与对应 `tool_use.id` 完全一致。

### tool_use 块结构

```json
{
  "type": "tool_use",
  "id": "toolu_01XyZ",
  "name": "get_weather",
  "input": {
    "city": "上海",
    "unit": "celsius"
  }
}
```

### tool_result 块结构

```json
{
  "type": "tool_result",
  "tool_use_id": "toolu_01XyZ",
  "content": "25°C 晴，湿度 60%"
}
```

`content` 也可以是数组（含图片等）：

```json
{
  "type": "tool_result",
  "tool_use_id": "toolu_01XyZ",
  "content": [
    { "type": "text", "text": "天气数据如下：" },
    { "type": "image", "source": { "type": "base64", ... } }
  ]
}
```

---

## 8. Tool Choice

```json
"tool_choice": {
  "type": "auto",
  "disable_parallel_tool_use": false
}
```

| type | 行为 |
|------|------|
| `"auto"` | 模型自己决定用不用工具（默认） |
| `"any"` | 必须调用至少一个工具 |
| `"tool"` | 强制调用指定工具（配合 `name` 字段） |
| `"none"` | 禁止调用任何工具 |

```json
// 强制调用指定工具
"tool_choice": { "type": "tool", "name": "get_weather" }

// 禁止并行调用，强制串行
"tool_choice": { "type": "auto", "disable_parallel_tool_use": true }
```

---

## 9. Extended Thinking

让模型在正式回答前进行内部推理，适合复杂推理、数学、编程等任务。

### 配置方式

```json
"thinking": {
  "type": "enabled",
  "budget_tokens": 6000
}
```

| 字段 | 可选值 | 说明 |
|------|--------|------|
| `type` | `"enabled"` / `"adaptive"` / `"disabled"` | 思考模式 |
| `budget_tokens` | int，≥ 1024 | 最大思考 token 数（`enabled` 模式必填） |
| `display` | `"summarized"` / `"omitted"` | 是否在响应中显示摘要（Opus 4.7+ 专属，默认 `"omitted"`） |

### 三种 type 模式

| 模式 | 说明 | 适用模型 |
|------|------|---------|
| `enabled` | 固定开启，必须指定 budget_tokens | claude-3-7-sonnet, claude-opus-4-6 等 |
| `adaptive` | 动态决定是否思考及思考多少 | claude-opus-4-6, claude-opus-4-7 |
| `disabled` | 关闭思考（默认） | 所有模型 |

### 使用 Thinking 的约束

```
✅ temperature 必须 = 1.0
✅ budget_tokens 必须 ≥ 1024
✅ budget_tokens 必须 < max_tokens
❌ top_p 不能设置（会被自动清除）
❌ top_k 不能设置（会被自动清除）
❌ Opus 4.7 不能设置 temperature / top_p / top_k

Thinking + Tool 同时使用时：
✅ 请求头必须加 anthropic-beta: interleaved-thinking-2025-05-14
```

### Thinking 响应块

```json
{
  "type": "thinking",
  "thinking": "用户问的是一个数学问题，我需要先分析...\n步骤一：...",
  "signature": "EqoBCkgIARAAGAIiQL..."
}
```

- `thinking`：模型的推理过程，人类可读
- `signature`：Anthropic 签名，**多轮对话时必须原样回传，禁止修改**

### budget_tokens 推荐值

```
简单推理任务：1024 ~ 2048
中等复杂任务：2048 ~ 6000
高难度推理  ：6000 ~ 16000
```

### 用模型名后缀快速开启（new-api 特有）

在 new-api 中，可以在模型名后加 `-thinking` 后缀，系统自动开启 Thinking：

```
claude-opus-4-6-thinking   → 自动 type=enabled, budget=max_tokens×80%
claude-opus-4-7-thinking   → 自动 type=adaptive, effort=high
```

### Effort 模式（adaptive 专属）

```json
"thinking": { "type": "adaptive" },
"output_config": { "effort": "high" }
```

| effort | budget_tokens 等效 | 说明 |
|--------|-------------------|------|
| `"low"` | ~1280 | 快速，省 token |
| `"medium"` | ~2048 | 均衡 |
| `"high"` | ~4096 | 最深入推理 |

---

## 10. 提示词缓存 cache_control

把固定内容（system 提示词、长文档、few-shot 示例）缓存起来，后续请求命中缓存可节省大量 token 费用。

### 打缓存标记

在 content 块末尾加 `cache_control`：

```json
"system": [
  {
    "type": "text",
    "text": "这是一份非常长的产品说明文档...[10000 token 内容]",
    "cache_control": { "type": "ephemeral" }
  }
]
```

```json
"messages": [
  {
    "role": "user",
    "content": [
      {
        "type": "text",
        "text": "以下是参考资料...[长内容]",
        "cache_control": { "type": "ephemeral" }
      },
      { "type": "text", "text": "基于以上资料，回答我的问题：..." }
    ]
  }
]
```

### 缓存类型

| 类型 | 保留时长 | 写入费用 | 命中费用 |
|------|---------|---------|---------|
| `ephemeral`（5分钟） | 5 分钟 | 标准价 × 1.05 | 标准价 × 0.1 |
| `ephemeral`（1小时） | 1 小时 | 标准价 × 1.25 | 标准价 × 0.1 |

> 缓存命中时输入成本约降至原来的 **1/10**，长文档场景下收益非常显著。

### 缓存策略建议

```
✅ 缓存 system 提示词（每次请求都不变）
✅ 缓存 few-shot 示例
✅ 缓存参考文档、背景知识
❌ 不要缓存会频繁变化的内容
❌ 缓存内容至少要有 1024 token 才值得缓存（太短不生效）
```

---

## 11. Web Search Tool

Anthropic 内置的联网搜索工具，无需自己实现，模型直接调用。

### 添加 Web Search Tool

```json
"tools": [
  {
    "type": "web_search_20250305",
    "name": "web_search",
    "max_uses": 5,
    "user_location": {
      "type": "approximate",
      "country": "CN",
      "city": "Shanghai",
      "region": "Shanghai",
      "timezone": "Asia/Shanghai"
    }
  }
]
```

| 字段 | 说明 |
|------|------|
| `type` | 固定值 `"web_search_20250305"` |
| `name` | 固定值 `"web_search"` |
| `max_uses` | 单次请求最多搜索次数（1/5/10） |
| `user_location` | 用户位置，影响搜索结果本地化 |

### max_uses 参考值

| 场景 | max_uses |
|------|---------|
| 简单事实查询 | 1 |
| 一般研究任务 | 5 |
| 深度研究 | 10 |

### 计费

Web Search 按搜索次数计费，响应 usage 里的 `server_tool_use.web_search_requests` 记录实际调用次数。

---

## 12. 高级字段

### output_config

控制模型输出行为，目前主要用于 adaptive thinking 的 effort 设置：

```json
"output_config": { "effort": "high" }
```

### metadata

```json
"metadata": { "user_id": "user_abc123" }
```

Anthropic 用于滥用检测，不影响模型行为。建议传入你系统里的用户 ID。

### 受保护字段（需 Channel 配置开启才透传）

| 字段 | 说明 |
|------|------|
| `inference_geo` | 数据驻留区域，如 `"EU"` |
| `speed` | 推理速度模式 |
| `service_tier` | 服务等级（影响优先级和计费） |

---

## 13. 响应结构

### 非流式响应

```json
{
  "id": "msg_01XyZ",
  "type": "message",
  "role": "assistant",
  "model": "claude-opus-4-6-20250514",
  "stop_reason": "end_turn",
  "stop_sequence": null,
  "content": [
    {
      "type": "thinking",
      "thinking": "让我思考一下这个问题...",
      "signature": "EqoBCkgIARAAGAIiQL..."
    },
    {
      "type": "text",
      "text": "根据分析，答案是..."
    }
  ],
  "usage": {
    "input_tokens": 150,
    "output_tokens": 320,
    "cache_creation_input_tokens": 80,
    "cache_read_input_tokens": 0
  }
}
```

### stop_reason 含义

| 值 | 含义 | 处理建议 |
|----|------|---------|
| `end_turn` | 模型正常结束 | 正常处理响应 |
| `max_tokens` | 达到 max_tokens 上限 | 可能需要增大 max_tokens 或继续对话 |
| `stop_sequence` | 命中 stop_sequences | 正常，按业务逻辑处理 |
| `tool_use` | 模型发起工具调用 | 执行工具，将结果加入 messages 后继续请求 |
| `refusal` | 模型拒绝回答 | 内容违规或安全限制 |

### 流式响应（SSE）

流式响应按事件推送，每行格式：`data: {json}`

```
event: message_start
data: {"type":"message_start","message":{"id":"msg_01","type":"message","role":"assistant","content":[],"model":"claude-opus-4-6",...}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"让我思考..."}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: content_block_start
data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"答案是..."}}

event: content_block_stop
data: {"type":"content_block_stop","index":1}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":320}}

event: message_stop
data: {"type":"message_stop"}
```

---

## 14. Token 用量与计费

### usage 字段详解

```json
"usage": {
  "input_tokens": 150,
  "output_tokens": 320,
  "cache_creation_input_tokens": 80,
  "cache_read_input_tokens": 0,
  "cache_creation": {
    "ephemeral_5m_input_tokens": 80,
    "ephemeral_1h_input_tokens": 0
  },
  "server_tool_use": {
    "web_search_requests": 2
  }
}
```

| 字段 | 含义 | 计费倍率（相对标准输入价） |
|------|------|--------------------------|
| `input_tokens` | 普通输入 token | 1× |
| `output_tokens` | 输出 token | 3× ~ 5×（按模型） |
| `cache_creation_input_tokens` | 写入缓存的 token | 1.25× |
| `cache_read_input_tokens` | 命中缓存读取的 token | 0.1× |
| `ephemeral_5m_input_tokens` | 5 分钟缓存写入 | 1.05× |
| `ephemeral_1h_input_tokens` | 1 小时缓存写入 | 1.25× |
| `web_search_requests` | Web Search 调用次数 | 按次计费 |

### 各模型定价参考（new-api 内部 ratio 换算）

> 1 ratio unit = $0.002 / 1K tokens

| 模型系列 | Ratio | 等效价格 |
|---------|-------|---------|
| claude-3-haiku | 0.125 | $0.25/1M tokens |
| claude-3-5-haiku / claude-haiku-4-5 | 0.5 | $1/1M tokens |
| claude-3/3.5/3.7-sonnet，sonnet-4/4-5 | 1.5 | $3/1M tokens |
| claude-opus-4-5/4-6/4-7 | 2.5 | $5/1M tokens |
| claude-3-opus，claude-opus-4/4-1 | 7.5 | $15/1M tokens |

---

## 15. 完整请求示例

以下是一个包含 Thinking + Tool + Web Search + 缓存 + 图片的复杂请求：

```json
POST /v1/messages
x-api-key: sk-ant-xxx
anthropic-version: 2023-06-01
anthropic-beta: interleaved-thinking-2025-05-14

{
  "model": "claude-opus-4-6",
  "max_tokens": 8192,
  "temperature": 1.0,
  "stop_sequences": ["<END>"],
  "stream": true,

  "system": [
    {
      "type": "text",
      "text": "你是一个专业的市场分析师，请用中文回答，数据引用要注明来源。",
      "cache_control": { "type": "ephemeral" }
    }
  ],

  "messages": [
    {
      "role": "user",
      "content": [
        { "type": "text", "text": "请分析这张销售图表，并查询最新市场趋势。" },
        {
          "type": "image",
          "source": {
            "type": "base64",
            "media_type": "image/png",
            "data": "iVBORw0KGgoAAAANSUhEUgAAAAE..."
          }
        }
      ]
    },
    {
      "role": "assistant",
      "content": [
        {
          "type": "thinking",
          "thinking": "用户上传了销售图表，需要分析图表内容并联网查询最新市场数据...",
          "signature": "EqoBCkgIARAAGAIiQL..."
        },
        {
          "type": "tool_use",
          "id": "toolu_01XyZ",
          "name": "get_market_data",
          "input": { "category": "electronics", "region": "CN" }
        }
      ]
    },
    {
      "role": "user",
      "content": [
        {
          "type": "tool_result",
          "tool_use_id": "toolu_01XyZ",
          "content": "{\"growth\": \"12.5%\", \"top_product\": \"智能手机\", \"revenue\": \"480亿\"}"
        }
      ]
    }
  ],

  "tools": [
    {
      "name": "get_market_data",
      "description": "查询指定品类和地区的最新市场数据，包括增长率、销售额等",
      "input_schema": {
        "type": "object",
        "properties": {
          "category": {
            "type": "string",
            "description": "商品品类，如 electronics、clothing、food"
          },
          "region": {
            "type": "string",
            "description": "地区代码，如 CN、US、EU"
          }
        },
        "required": ["category", "region"]
      }
    },
    {
      "type": "web_search_20250305",
      "name": "web_search",
      "max_uses": 3,
      "user_location": {
        "type": "approximate",
        "country": "CN",
        "city": "Shanghai",
        "timezone": "Asia/Shanghai"
      }
    }
  ],

  "tool_choice": {
    "type": "auto",
    "disable_parallel_tool_use": false
  },

  "thinking": {
    "type": "enabled",
    "budget_tokens": 6553
  },

  "metadata": {
    "user_id": "user_abc123"
  }
}
```

---

## 16. 参数约束速查表

| 场景 | 约束 |
|------|------|
| 使用 Thinking | temperature = 1.0，top_p 和 top_k 必须为空 |
| 使用 Thinking + Tool | 请求头加 `anthropic-beta: interleaved-thinking-2025-05-14` |
| Opus 4.7 的 Thinking | 只支持 `adaptive`，不支持 `enabled`；temperature/top_p/top_k 均不能设置 |
| budget_tokens | 必须 ≥ 1024，必须 < max_tokens |
| tool_result | 必须放在 user 消息里 |
| tool_use_id | 必须与对应 tool_use 的 id 完全一致 |
| thinking.signature | 多轮对话时必须原样回传，不能修改 |
| stop_sequences | 最多 4 个 |
| 图片 URL on Bedrock | 自动转换为 base64，无需手动处理 |
| messages 开头 | 第一条必须是 user 消息 |
| 相邻同角色消息 | 自动合并为一条 |

---

## 17. 多轮对话 Tool 调用流程图

```
你的代码                                    Claude API
   │                                            │
   │──── 第 1 次请求 ──────────────────────────>│
   │     messages: [user: "查天气"]             │
   │                                            │ 模型决定调用工具
   │<─── 第 1 次响应 ────────────────────────── │
   │     stop_reason: "tool_use"                │
   │     content: [tool_use{id, name, input}]   │
   │                                            │
   │ 执行工具：get_weather(上海)                 │
   │ 得到结果：25°C 晴                           │
   │                                            │
   │──── 第 2 次请求 ──────────────────────────>│
   │     messages: [                            │
   │       user: "查天气",                      │
   │       assistant: [tool_use{id,...}],       │  ← 原样回传上一轮 assistant 消息
   │       user: [tool_result{tool_use_id,...}] │  ← 追加工具结果
   │     ]                                      │
   │                                            │ 模型生成最终回答
   │<─── 第 2 次响应 ────────────────────────── │
   │     stop_reason: "end_turn"                │
   │     content: [text: "上海现在 25°C，晴天"] │
   │                                            │
```

---

## 18. 模型列表与能力矩阵

### Haiku 系列（轻量快速）

| 模型 | 上下文 | Thinking | 视觉 | 工具调用 |
|------|--------|---------|------|---------|
| `claude-3-haiku-20240307` | 200K | ❌ | ✅ | ✅ |
| `claude-3-5-haiku-20241022` | 200K | ❌ | ✅ | ✅ |
| `claude-haiku-4-5-20251001` | 200K | ❌ | ✅ | ✅ |

### Sonnet 系列（均衡主力）

| 模型 | 上下文 | Thinking | 视觉 | 工具调用 |
|------|--------|---------|------|---------|
| `claude-3-sonnet-20240229` | 200K | ❌ | ✅ | ✅ |
| `claude-3-5-sonnet-20240620` | 200K | ❌ | ✅ | ✅ |
| `claude-3-5-sonnet-20241022` | 200K | ❌ | ✅ | ✅ |
| `claude-3-7-sonnet-20250219` | 200K | ✅ enabled | ✅ | ✅ |
| `claude-sonnet-4-20250514` | 200K | ✅ enabled | ✅ | ✅ |
| `claude-sonnet-4-5-20250929` | 200K | ✅ enabled | ✅ | ✅ |

### Opus 系列（最强推理）

| 模型 | 上下文 | Thinking | 视觉 | 工具调用 |
|------|--------|---------|------|---------|
| `claude-3-opus-20240229` | 200K | ❌ | ✅ | ✅ |
| `claude-opus-4-20250514` | 200K | ✅ enabled | ✅ | ✅ |
| `claude-opus-4-1-20250805` | 200K | ✅ enabled | ✅ | ✅ |
| `claude-opus-4-5-20251101` | 200K | ✅ enabled/adaptive | ✅ | ✅ |
| `claude-opus-4-6` | 200K | ✅ enabled/adaptive | ✅ | ✅ |
| `claude-opus-4-7` | 200K | ✅ adaptive only | ✅ | ✅ |

### 模型选型建议

```
高并发、低延迟、低成本  →  claude-haiku-4-5
日常问答、代码助手      →  claude-sonnet-4-5
复杂推理、深度分析      →  claude-opus-4-6（成本/性能均衡）
最高推理能力            →  claude-opus-4-7（adaptive thinking）
```

---

## 附录：常见错误排查

| 错误 | 原因 | 解决方案 |
|------|------|---------|
| `temperature must be 1.0 when thinking is enabled` | Thinking 模式下 temperature 不是 1.0 | 设置 `temperature: 1.0` |
| `budget_tokens must be at least 1024` | thinking.budget_tokens 太小 | 设置 ≥ 1024 |
| `tool_use_id not found` | tool_result 的 id 对不上 | 检查 tool_use_id 与 tool_use.id 是否一致 |
| `first message must be user` | messages 第一条是 assistant | 在前面插入一条 user 消息 |
| `anthropic-version header is required` | 缺少版本头 | 添加 `anthropic-version: 2023-06-01` |
| `interleaved thinking not supported` | Thinking + Tool 但没加 beta header | 添加 `anthropic-beta: interleaved-thinking-2025-05-14` |
