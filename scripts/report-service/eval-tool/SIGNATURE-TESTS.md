# Thinking Signature 校验测试套件

> 给定一个 Anthropic Messages API 兼容 endpoint，验证它对 `thinking` block 上的 `signature` 字段是否做密码学校验。Anthropic 官方文档明确："Modifying any part of the thinking block (including the text or signature) will result in an error."

## 凭据

跟 PIPELINE.md 共用 `tools/provider-eval/.env`：

```bash
URL=https://your-endpoint.example/v1
KEY=sk-your-api-key
MODEL=claude-sonnet-4-6   # 大多数 signature 脚本默认 claude-sonnet-4-6，可用 MODEL_OVERRIDE 覆盖
```

`.env` 已 gitignored。脚本不接受命令行传入 key，避免 key 进 shell history / process list。

## 脚本一览

| 脚本                             | 目的                                                                                                                                           | 关键变量                                                                |
| -------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------- |
| `signature-tamper.mjs`           | **非 tool_use** 多轮场景下，跑 7 种 signature 变体（valid / tail flip 8 / tail flip 32 / middle flip 16 / truncated / random / empty）× N 并发 | `MODEL_OVERRIDE`                                                        |
| `signature-tamper-tooluse.mjs`   | **tool_use 闭环** 场景下跑同样 7 种变体（Anthropic 文档明确说这是 signature 校验严格生效的场景）                                               | `MODEL_OVERRIDE`                                                        |
| `signature-tamper-opus.mjs`      | claude-opus-4-7 的 **adaptive thinking API**（`thinking.adaptive` + `output_config.effort=max`），non-tool 和 tool 场景都跑                    | `MODEL_OVERRIDE` / `SCENARIO=both\|text\|tooluse`                       |
| `signature-leak-rate.mjs`        | 单变体高 N 估算漏率（空 signature × N=50 默认）                                                                                                | `MODEL_OVERRIDE` / `N` / `BATCH`                                        |
| `signature-leak-fingerprint.mjs` | 同上，但抓**完整 response headers**，对 200 vs 400 两组做 presence-majority diff 找单请求级别 fingerprint                                      | `MODEL_OVERRIDE` / `N` / `BATCH`                                        |
| `signature-leak-body.mjs`        | 抓**完整 response body**，跟 baseline (valid signature) 做结构差分（leak 是否缺 thinking block、缺 cache 字段、token count 是否偏离）          | `MODEL_OVERRIDE` / `N` / `BATCH` / `TAMPER=empty\|tail8\|tail32\|mid16` |
| `latency-baseline.mjs`           | 不同请求 shape（trivial ping / 短题无 thinking / 短题有 thinking / 中等推理）的端到端延迟基线                                                  | `MODEL_OVERRIDE` / `N`                                                  |

## 典型工作流

```bash
# 1. 快速确认 endpoint 是否做 signature 校验
MODEL_OVERRIDE=claude-sonnet-4-6 node tools/provider-eval/signature-tamper.mjs
# - 7 个 variant 全 200 → endpoint 完全不校验
# - tampered 全 400 → endpoint 严格校验
# - 部分 200 部分 400 → 混合路由

# 2. 如果观察到 leak，量化漏率
MODEL_OVERRIDE=claude-sonnet-4-6 N=50 BATCH=10 node tools/provider-eval/signature-leak-rate.mjs

# 3. 找 per-request fingerprint（200 vs 400 在 header 上的差异）
MODEL_OVERRIDE=claude-sonnet-4-6 N=50 BATCH=10 node tools/provider-eval/signature-leak-fingerprint.mjs
# 输出：tools/provider-eval/leak-fingerprint-data.json

# 4. 抓 leak 的 response body 结构（确认 leak 后端是否 Anthropic 原生 shape）
MODEL_OVERRIDE=claude-sonnet-4-6 TAMPER=empty N=30 node tools/provider-eval/signature-leak-body.mjs
# 输出：tools/provider-eval/leak-bodies-empty.json

# 5. 用同长度合法 base64 篡改（封死"空字符串明显非法所以放过"的辩解口径）
MODEL_OVERRIDE=claude-sonnet-4-6 TAMPER=tail8 N=30 node tools/provider-eval/signature-leak-body.mjs
# 输出：tools/provider-eval/leak-bodies-tail8.json

# 6. tool_use 闭环验证（防止有人说"非 tool_use 场景 Anthropic 本就不严格校验"）
MODEL_OVERRIDE=claude-sonnet-4-6 node tools/provider-eval/signature-tamper-tooluse.mjs
```

## TAMPER 模式（signature-leak-body.mjs）

| 模式           | 改动                                    | 用途                                    |
| -------------- | --------------------------------------- | --------------------------------------- |
| `empty` (默认) | signature → `""`                        | 最明显的非法值                          |
| `tail8`        | 总长不变，末尾 8 个 base64 字符随机替换 | 保持长度+合法字符集，仅破坏密码学完整性 |
| `tail32`       | 末尾 32 个字符随机                      | 篡改幅度加大                            |
| `mid16`        | 中间 16 个字符随机替换                  | 不影响开头的 protobuf 头部，破坏负载    |

## 输出文件

所有数据文件 `leak-*.json` 和报告目录 `reports/` 都在 `.gitignore` 里，**不会上传**。每次重跑会**覆盖**同名文件。

| 路径                                                     | 内容                                                        |
| -------------------------------------------------------- | ----------------------------------------------------------- |
| `leak-bodies-{TAMPER}.json`                              | `signature-leak-body.mjs` 的完整 body + headers             |
| `leak-fingerprint-data.json`                             | `signature-leak-fingerprint.mjs` 的完整 headers + body 摘要 |
| `reports/<endpoint>-<model>-<date>-signature-bypass*.md` | 人工分析报告                                                |
| `reports/request-ids-for-supplier-*.md`                  | 给供应商排查的 ID 汇总                                      |

## 解读规则速查

### tampered signature 的 4 种典型响应模式

| 模式                              | 200 / 400 分布                              | 含义                                                                                                                                                             |
| --------------------------------- | ------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **A. 全 400**                     | 0% leak                                     | upstream 真在做 signature 校验（典型 Anthropic 直供 / Bedrock / Vertex）                                                                                         |
| **B. 全 200**                     | 100% leak                                   | upstream 不校验，或网关在转发前 strip 掉了 signature                                                                                                             |
| **C. 部分 200 部分 400**          | 5-30% leak                                  | 网关混合路由：部分请求落到严格 backend、部分落到宽松 backend                                                                                                     |
| **D. 全 400 但漏过的 200 显著慢** | 5-20% leak，且 leak latency >> 严格 latency | 网关有 strip-and-retry 回退逻辑（如 LiteLLM `is_anthropic_invalid_thinking_signature_error()` + `strip_thinking_blocks_from_anthropic_messages_request_dict()`） |

### Leak 后端的 body-level 指纹（针对 Anthropic 原生 shape）

真 Anthropic API 总是返回：

```json
"usage": {
  "input_tokens": N,
  "cache_creation_input_tokens": 0,   // 即使没用 cache 也存在
  "cache_read_input_tokens": 0,
  "cache_creation": {
    "ephemeral_5m_input_tokens": 0,
    "ephemeral_1h_input_tokens": 0
  },
  ...
}
```

如果 leak 后端的 `usage` **缺这套 cache 字段**，几乎可以确定 upstream 不是真 Anthropic（合规的 Bedrock / Vertex 也都会返回这套字段）。

如果请求带了 `thinking: enabled` 但 leak response.content 里**没有 thinking block**，也是同源问题——真 Anthropic API 在 thinking enabled 时必定返回 thinking block。

## 已知 endpoint 行为档案

| Endpoint                  | 网关                              | 模式                         | 来源                                   |
| ------------------------- | --------------------------------- | ---------------------------- | -------------------------------------- |
| `maasapi.anispark.ai/llm` | LiteLLM 1.83.10                   | **D（strip-and-retry）**     | 测试日 2026-05-20，见 PR #15501 上下文 |
| `qy.ogog.ai`              | new-api v1.2.7 (OGOG.AI 企业对接) | **C（混合路由，~17% 漏率）** | 测试日 2026-05-20                      |
| `15.204.106.173:3000`     | 同上后端直连 IP                   | **B（100% 漏率）**           | 测试日 2026-05-20                      |
