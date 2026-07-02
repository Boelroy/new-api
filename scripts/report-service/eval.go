package main

// Spawns the bundled `eval-tool/probe.mjs` as a Node subprocess and returns
// the full Markdown trace bundle once the child exits. stderr is streamed
// per-line via the onStderr callback so the caller can surface live progress
// in a UI.

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

const (
	evalDefaultRepeat       = 1
	evalMaxRepeat           = 5
	evalTraceMaxBytes       = 8 * 1024 * 1024 // ~8 MiB ceiling on the trace
	evalDefaultScriptEnvVar = "EVAL_SCRIPT_PATH"
)

func evalScriptPath() string {
	if p := strings.TrimSpace(os.Getenv(evalDefaultScriptEnvVar)); p != "" {
		return p
	}
	if exe, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(exe), "eval-tool", "probe.mjs")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return "eval-tool/probe.mjs"
}

// runEvalProbe runs `node probe.mjs --url ... --key ... --model ... --out -`
// and returns the trace markdown on stdout. The supplied context governs
// cancellation/timeout. Lines from stderr are forwarded to onStderr (if
// non-nil) as they arrive. runID (when non-empty) is passed via --runid so
// probe.mjs uses it as the cache-bust salt + seeded-RNG seed — that keeps
// each report-service run's probes distinct from prior traces and makes
// the same run reproducible on retry.
func runEvalProbe(ctx context.Context, url, key, model, runID string, repeat int, onStderr func(string)) (string, error) {
	script := evalScriptPath()
	if _, err := os.Stat(script); err != nil {
		return "", fmt.Errorf("probe.mjs not found at %s: %w", script, err)
	}
	if repeat <= 0 {
		repeat = evalDefaultRepeat
	}
	if repeat > evalMaxRepeat {
		repeat = evalMaxRepeat
	}

	args := []string{script,
		"--url", url,
		"--key", key,
		"--model", model,
		"--out", "-",
	}
	if repeat > 1 {
		args = append(args, "--repeat", strconv.Itoa(repeat))
	}
	if runID = strings.TrimSpace(runID); runID != "" {
		args = append(args, "--runid", runID)
	}

	cmd := exec.CommandContext(ctx, "node", args...)
	// Don't leak the upstream key via env; probe.mjs takes everything via flags.
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "TZ=" + os.Getenv("TZ")}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return "", fmt.Errorf("stderr pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("spawn node: %w", err)
	}

	var wg sync.WaitGroup
	if onStderr != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sc := bufio.NewScanner(stderr)
			sc.Buffer(make([]byte, 64*1024), 256*1024)
			for sc.Scan() {
				onStderr(sc.Text())
			}
		}()
	} else {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = io.Copy(io.Discard, stderr)
		}()
	}

	traceBuf, readErr := io.ReadAll(io.LimitReader(stdout, evalTraceMaxBytes+1))
	wg.Wait()
	waitErr := cmd.Wait()

	truncated := len(traceBuf) > evalTraceMaxBytes
	if truncated {
		traceBuf = traceBuf[:evalTraceMaxBytes]
		if onStderr != nil {
			onStderr("<trace truncated at 8 MiB>")
		}
	}

	if waitErr != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return string(traceBuf), errors.New("evaluation timed out")
		}
		if errors.Is(ctx.Err(), context.Canceled) {
			return string(traceBuf), errors.New("evaluation cancelled")
		}
		return string(traceBuf), waitErr
	}
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return string(traceBuf), fmt.Errorf("read stdout: %w", readErr)
	}
	return string(traceBuf), nil
}
