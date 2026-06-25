package main

// Claude Code (headless) grader. Spawns `claude -p` with a prompt assembled
// from PIPELINE.md + the trace and returns the Markdown report. Auth is via
// CLAUDE_GRADER_API_KEY → ANTHROPIC_API_KEY in the child env. The CLI itself
// is installed in the runtime Dockerfile (`npm i -g @anthropic-ai/claude-code`).

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const (
	graderEnvAPIKey     = "CLAUDE_GRADER_API_KEY"
	graderEnvAuthToken  = "CLAUDE_GRADER_AUTH_TOKEN"
	graderEnvBaseURL    = "CLAUDE_GRADER_BASE_URL"
	graderEnvModel      = "CLAUDE_GRADER_MODEL"
	graderEnvTimeoutSec = "CLAUDE_GRADER_TIMEOUT_SEC"
	graderDefaultModel  = "claude-sonnet-4-6"
	// Bumped from 5min to 15min: full eval graders through gateways with
	// 192KB prompts can take 6-10min in practice. Overridable via env.
	graderHardTimeout   = 15 * time.Minute
	graderMaxPromptSize = 1 << 20 // 1 MiB — pipeline + trace combined safety cap
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
2. Trace — probe.mjs 产出的完整 trace（~47 个 probe，覆盖 25 步）

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

// graderConfigured returns true when either a direct API key or a
// (auth-token + optional base-url) gateway combo is configured. UI uses
// this to default the "auto-analyze" toggle.
func graderConfigured() bool {
	return strings.TrimSpace(os.Getenv(graderEnvAPIKey)) != "" ||
		strings.TrimSpace(os.Getenv(graderEnvAuthToken)) != ""
}

// runClaudeGrader spawns `claude -p` with the given prompt piped on stdin
// and returns its stdout. Times out at graderHardTimeout. Returns an
// error wrapping stderr when claude exits non-zero or the prompt is empty.
func runClaudeGrader(ctx context.Context, instruction, pipelineMD, traceMD string) (string, error) {
	apiKey := strings.TrimSpace(os.Getenv(graderEnvAPIKey))
	authToken := strings.TrimSpace(os.Getenv(graderEnvAuthToken))
	baseURL := strings.TrimSpace(os.Getenv(graderEnvBaseURL))
	if apiKey == "" && authToken == "" {
		return "", errors.New("CLAUDE_GRADER_API_KEY or CLAUDE_GRADER_AUTH_TOKEN not configured")
	}
	model := strings.TrimSpace(os.Getenv(graderEnvModel))
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
		// Truncate trace from the end if we exceed the safety cap.
		over := prompt.Len() - graderMaxPromptSize
		traceCut := len(traceMD) - over - 256 // leave a buffer
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
	jobCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(jobCtx, "claude", "-p", "--model", model, "--output-format", "text")
	// Don't inherit the entire env (which may already have ANTHROPIC_API_KEY pointing at the probed key).
	childEnv := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
		"TZ=" + os.Getenv("TZ"),
	}
	if apiKey != "" {
		childEnv = append(childEnv, "ANTHROPIC_API_KEY="+apiKey)
	}
	if authToken != "" {
		// Bearer-style auth used by Anthropic-compatible gateways. Claude CLI
		// prefers this over ANTHROPIC_API_KEY when both are set.
		childEnv = append(childEnv, "ANTHROPIC_AUTH_TOKEN="+authToken)
	}
	if baseURL != "" {
		childEnv = append(childEnv, "ANTHROPIC_BASE_URL="+baseURL)
	}
	cmd.Env = childEnv
	cmd.Stdin = bytes.NewReader(prompt.Bytes())

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		// Trim noisy stderr so the UI message stays readable.
		stderrPreview := strings.TrimSpace(stderr.String())
		if len(stderrPreview) > 800 {
			stderrPreview = stderrPreview[:800] + "..."
		}
		if errors.Is(jobCtx.Err(), context.DeadlineExceeded) {
			return "", fmt.Errorf("claude grader timed out after %s (stderr: %s)", graderHardTimeout, stderrPreview)
		}
		return "", fmt.Errorf("claude grader exited with error: %w (stderr: %s)", err, stderrPreview)
	}
	return strings.TrimSpace(stdout.String()), nil
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
