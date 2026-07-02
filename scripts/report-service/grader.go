package main

// Claude grader. Given a project's user-supplied grader URL + api-key +
// (optional) model, POST the assembled prompt to /v1/messages non-stream
// and return the concatenated text response. Empty grader creds mean
// "skip grading" — checked at each call site.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	graderEnvTimeoutSec = "CLAUDE_GRADER_TIMEOUT_SEC"
	graderDefaultModel  = "claude-sonnet-4-6"
	// 15min matches the previous CLI-based timeout — full eval traces at
	// ~200KB regularly need 6-10 minutes on real gateways.
	graderHardTimeout   = 15 * time.Minute
	graderMaxPromptSize = 1 << 20 // 1 MiB — pipeline + trace combined safety cap
	graderMaxTokens     = 4096    // report.md rarely exceeds 3KB; keep some slack
)

// detectGraderPrompt prompts the LLM with provider-detection PIPELINE.md +
// our trace and asks for the §0.3 report in Chinese.
const detectGraderInstruction = `你是 Claude / Anthropic 兼容 API endpoint 的**供应商判别助手**。

下面会附上两份资料：
1. PIPELINE.md — provider-detection 方法论 + §2 信号面板（A1/A2/A3/A4/B1-B6）
2. Trace — 6 个 probe 的 HTTP 抓包（GET /v1/models / plain / tools / stream / huge-max_tokens / invalid-role）

请按 PIPELINE.md §0.3 的方法分析 trace，用**简体中文**输出报告，严格按以下结构（30-60 行）：

# Provider Detection Report

## 结论

| 层      | 标签                                    | Confidence       |
| ------- | --------------------------------------- | ---------------- |
| Router  | <e.g. new-api fork / OpenRouter / 直连>  | high/medium/low  |
| Backend | <Anthropic 1P / AWS Bedrock / GCP Vertex>| high/medium/low  |

## 命中信号

（每条引用 §2 编号：A1 / A2 / A3 / A4 / B1 / B2 / ... / B6，并写明在哪个 probe 看到了什么）

## 反信号 / 矛盾点

（如果有）

## 不确定的部分

（如果信号不够，建议补什么 probe）

只输出报告本身，不要任何 meta 说明、寒暄或代码块包裹。`

// evalGraderInstruction prompts for the §0.3-§0.4 provider-eval report.
const evalGraderInstruction = `你是 LLM endpoint **评估助手**。

下面会附上两份资料：
1. PIPELINE.md — provider-eval 方法论 + §2 性能 / §3 智商 / §4 功能信号面板 + §6 评分规则
2. Trace — probe.mjs 产出的完整 trace（覆盖 §2/§3/§4 全部维度的多组 probe）

请按 PIPELINE.md §0.3-§0.4 分析 trace，用**简体中文**输出三维评估报告：

# Provider Evaluation Report

## 结论

| 维度 | 档位 | 备注 |
| --- | --- | --- |
| 性能 | A / B / C / D | 一句话 |
| 智商 | A / B / C / D | 一句话 |
| 功能 | A / B / C / D | 一句话 |
| 总评 | A / B / C / D | (按 §6.2 加权) |

## 维度 1：性能

（命中 §2 信号 + 关键数字：TTFB、TTFT、长输出 throughput、流式 max gap）

## 维度 2：智商

（按 §3 列表逐题判定 strict / loose / fail，列出失败的题号）

## 维度 3：功能

（按 §4 信号面板：streaming / tool / caching / json_schema / 1M context / vision / thinking 等覆盖度）

## 风险 / 异常

（如果有信号矛盾、超时、5xx、缓存失效、签名异常等）

只输出报告本身，不要任何 meta 说明、寒暄或代码块包裹。`

// graderCredsPresent returns true when a project has both grader URL and
// api-key set. Used to gate whether the grader step runs at all.
func graderCredsPresent(url, apiKey string) bool {
	return strings.TrimSpace(url) != "" && strings.TrimSpace(apiKey) != ""
}

// graderConfigured is a legacy shim retained so /api/auth/config keeps a
// stable field name. Grader creds are now per-project, so from the config
// endpoint's perspective grader is always "available" — the actual gate
// is graderCredsPresent(project.grader_url, project.grader_api_key).
func graderConfigured() bool { return true }

// runDirectHTTPGrader POSTs the assembled (instruction + PIPELINE.md +
// trace) prompt as a single user message to {graderURL}/v1/messages and
// returns the concatenated text response. Empty creds → error (callers
// should check graderCredsPresent first and skip the call altogether).
func runDirectHTTPGrader(
	ctx context.Context,
	graderURL, graderAPIKey, graderModel string,
	instruction, pipelineMD, traceMD string,
) (string, error) {
	graderURL = strings.TrimSpace(graderURL)
	graderAPIKey = strings.TrimSpace(graderAPIKey)
	if graderURL == "" || graderAPIKey == "" {
		return "", errors.New("grader URL and api key required")
	}
	model := strings.TrimSpace(graderModel)
	if model == "" {
		model = graderDefaultModel
	}

	var prompt bytes.Buffer
	prompt.WriteString(instruction)
	prompt.WriteString("\n\n---\n\n# PIPELINE.md\n\n")
	prompt.WriteString(pipelineMD)
	prompt.WriteString("\n\n---\n\n# Trace\n\n")
	prompt.WriteString(traceMD)

	if prompt.Len() > graderMaxPromptSize {
		over := prompt.Len() - graderMaxPromptSize
		traceCut := len(traceMD) - over - 256
		if traceCut < 0 {
			return "", fmt.Errorf("prompt too large (pipeline alone is %d bytes)", len(pipelineMD)+len(instruction))
		}
		prompt.Reset()
		prompt.WriteString(instruction)
		prompt.WriteString("\n\n---\n\n# PIPELINE.md\n\n")
		prompt.WriteString(pipelineMD)
		prompt.WriteString("\n\n---\n\n# Trace (truncated)\n\n")
		prompt.WriteString(traceMD[:traceCut])
		prompt.WriteString("\n\n<trace truncated>")
	}

	timeout := graderHardTimeout
	if v := strings.TrimSpace(os.Getenv(graderEnvTimeoutSec)); v != "" {
		if secs, perr := strconv.Atoi(v); perr == nil && secs > 0 && secs <= 3600 {
			timeout = time.Duration(secs) * time.Second
		}
	}

	body := map[string]any{
		"model":      model,
		"max_tokens": graderMaxTokens,
		"messages": []map[string]any{{
			"role": "user",
			"content": []map[string]any{
				{"type": "text", "text": prompt.String()},
			},
		}},
	}
	buf, err := json.Marshal(body)
	if err != nil {
		return "", err
	}

	endpoint := strings.TrimRight(graderURL, "/") + "/v1/messages"
	jobCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(jobCtx, "POST", endpoint, bytes.NewReader(buf))
	if err != nil {
		return "", err
	}
	req.Header.Set("x-api-key", graderAPIKey)
	req.Header.Set("anthropic-version", detectAnthropicVerHdr)
	req.Header.Set("content-type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("grader request failed: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		msg := strings.TrimSpace(string(respBody))
		if len(msg) > 800 {
			msg = msg[:800] + "..."
		}
		return "", fmt.Errorf("grader HTTP %d: %s", resp.StatusCode, msg)
	}

	var parsed struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return "", fmt.Errorf("grader parse response: %w", err)
	}
	var sb strings.Builder
	for _, blk := range parsed.Content {
		if blk.Type == "text" {
			sb.WriteString(blk.Text)
		}
	}
	out := strings.TrimSpace(sb.String())
	if out == "" {
		return "", errors.New("grader returned empty text")
	}
	return out, nil
}

// readPipelineFile loads a PIPELINE.md from disk via env var.
func readPipelineFile(envVar string) (string, error) {
	path := strings.TrimSpace(os.Getenv(envVar))
	if path == "" {
		return "", fmt.Errorf("%s not set", envVar)
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	buf, err := io.ReadAll(f)
	if err != nil {
		return "", err
	}
	return string(buf), nil
}
