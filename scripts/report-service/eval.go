package main

// Provider evaluation: spawn the bundled `eval-tool/probe.mjs` as a Node
// subprocess, stream its stderr (per-step progress messages) into a
// per-job buffer so the UI can poll, and capture stdout (the final
// Markdown trace bundle) once it completes.

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	evalDefaultRepeat       = 1
	evalMaxRepeat           = 5
	evalDefaultPerProbeMs   = 60_000           // matches probe.mjs default
	evalMaxWallClock        = 30 * time.Minute // hard cap on total runtime
	evalJobRetention        = 30 * time.Minute // how long completed jobs stay queryable
	evalStderrMaxBytes      = 256 * 1024       // bound the live log buffer
	evalTraceMaxBytes       = 8 * 1024 * 1024  // ~8 MiB ceiling on the trace
	evalDefaultScriptEnvVar = "EVAL_SCRIPT_PATH"
)

type evalJob struct {
	ID          string
	URL         string
	Model       string
	Repeat      int
	StartedAt   time.Time
	EndedAt     time.Time
	Status      string // "running" | "ok" | "error" | "cancelled"
	StderrBuf   strings.Builder
	StderrTrim  bool // true once we started dropping head bytes
	Trace       string
	Err         string
	RunGrader   bool
	LLMReport   string
	LLMError    string
	GraderMs    int64
	cancel      context.CancelFunc
	mu          sync.Mutex
}

var (
	evalJobs   = map[string]*evalJob{}
	evalJobsMu sync.Mutex
)

func evalScriptPath() string {
	if p := strings.TrimSpace(os.Getenv(evalDefaultScriptEnvVar)); p != "" {
		return p
	}
	// Dev fallback: look next to the binary.
	if exe, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(exe), "eval-tool", "probe.mjs")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	// Last resort — relative to CWD, useful when running `go run .` from the
	// report-service directory.
	return "eval-tool/probe.mjs"
}

func newEvalJobID() string {
	buf := make([]byte, 8)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}

// appendStderr keeps the live log bounded so a long pass@3 run doesn't blow up
// memory. When we exceed the cap we drop the oldest 25 % and mark the buffer
// trimmed so the UI can show a "...truncated" note.
func (j *evalJob) appendStderr(line string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.StderrBuf.Len()+len(line)+1 > evalStderrMaxBytes {
		// Drop oldest 25 %.
		s := j.StderrBuf.String()
		cut := len(s) / 4
		if cut < len(s) {
			j.StderrBuf.Reset()
			j.StderrBuf.WriteString(s[cut:])
			j.StderrTrim = true
		}
	}
	j.StderrBuf.WriteString(line)
	j.StderrBuf.WriteByte('\n')
}

type evalSnapshot struct {
	Status     string
	StderrLog  string
	Trace      string
	Err        string
	LLMReport  string
	LLMError   string
	GraderMs   int64
	StderrTrim bool
	Repeat     int
	StartedAt  time.Time
	EndedAt    time.Time
}

func (j *evalJob) snapshot() evalSnapshot {
	j.mu.Lock()
	defer j.mu.Unlock()
	return evalSnapshot{
		Status:     j.Status,
		StderrLog:  j.StderrBuf.String(),
		Trace:      j.Trace,
		Err:        j.Err,
		LLMReport:  j.LLMReport,
		LLMError:   j.LLMError,
		GraderMs:   j.GraderMs,
		StderrTrim: j.StderrTrim,
		Repeat:     j.Repeat,
		StartedAt:  j.StartedAt,
		EndedAt:    j.EndedAt,
	}
}

// runEval is the goroutine that drives `node probe.mjs ... --out -`.
// It writes incrementally into j.StderrBuf, captures stdout in full, and
// flips j.Status when the child exits.
func runEval(j *evalJob, url, key, model string, repeat int) {
	script := evalScriptPath()
	if _, err := os.Stat(script); err != nil {
		j.mu.Lock()
		j.Status = "error"
		j.Err = "probe.mjs not found at " + script + ": " + err.Error()
		j.EndedAt = time.Now()
		j.mu.Unlock()
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), evalMaxWallClock)
	j.mu.Lock()
	j.cancel = cancel
	j.mu.Unlock()
	defer cancel()

	args := []string{script,
		"--url", url,
		"--key", key,
		"--model", model,
		"--out", "-", // stream the final markdown to stdout
	}
	if repeat > 1 {
		args = append(args, "--repeat", strconv.Itoa(repeat))
	}

	cmd := exec.CommandContext(ctx, "node", args...)
	// Don't leak key via the environment — probe.mjs reads everything from flags.
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "TZ=" + os.Getenv("TZ")}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		j.fail("stdout pipe: " + err.Error())
		return
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		j.fail("stderr pipe: " + err.Error())
		return
	}
	if err := cmd.Start(); err != nil {
		j.fail("spawn node: " + err.Error())
		return
	}

	// Pump stderr line-by-line into the job's live log.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		scanner := bufio.NewScanner(stderr)
		scanner.Buffer(make([]byte, 64*1024), 256*1024)
		for scanner.Scan() {
			j.appendStderr(scanner.Text())
		}
	}()

	// Drain stdout fully — probe.mjs writes the entire trace at the end.
	traceBuf, readErr := io.ReadAll(io.LimitReader(stdout, evalTraceMaxBytes+1))
	wg.Wait()
	waitErr := cmd.Wait()

	if len(traceBuf) > evalTraceMaxBytes {
		traceBuf = traceBuf[:evalTraceMaxBytes]
		j.appendStderr("<trace truncated at 8 MiB>")
	}

	j.mu.Lock()
	j.Trace = string(traceBuf)
	probeDone := time.Now()
	switch {
	case waitErr != nil:
		j.Status = "error"
		switch {
		case errors.Is(ctx.Err(), context.DeadlineExceeded):
			j.Err = "evaluation timed out after " + evalMaxWallClock.String()
		case errors.Is(ctx.Err(), context.Canceled):
			j.Err = "evaluation cancelled"
		default:
			j.Err = waitErr.Error()
		}
	case readErr != nil && !errors.Is(readErr, io.EOF):
		j.Status = "error"
		j.Err = "read stdout: " + readErr.Error()
	default:
		j.Status = "grading" // bumped to ok after grader returns (or skip)
	}
	runGrader := j.RunGrader
	traceForGrader := j.Trace
	j.mu.Unlock()

	if j.Status == "grading" {
		if runGrader && graderConfigured() {
			j.appendStderr("--- probe complete, invoking Claude grader ---")
			pipelineMD, perr := readPipelineFile("EVAL_PIPELINE_PATH")
			if perr != nil {
				j.appendStderr("grader: pipeline load failed — " + perr.Error())
				j.mu.Lock()
				j.LLMError = "pipeline load: " + perr.Error()
				j.Status = "ok"
				j.EndedAt = time.Now()
				j.mu.Unlock()
				return
			}
			t0 := time.Now()
			// Detach grader from the wall-clock context (which is already past
			// the probe budget). Use a fresh root context — runClaudeGrader
			// applies its own hard timeout.
			report, gerr := runClaudeGrader(context.Background(), evalGraderInstruction, pipelineMD, traceForGrader)
			elapsed := time.Since(t0).Milliseconds()
			j.mu.Lock()
			j.GraderMs = elapsed
			if gerr != nil {
				j.appendStderr("grader: " + gerr.Error())
				j.LLMError = gerr.Error()
			} else {
				j.appendStderr(fmt.Sprintf("grader: done in %dms (%d chars)", elapsed, len(report)))
				j.LLMReport = report
			}
			j.Status = "ok"
			j.EndedAt = time.Now()
			j.mu.Unlock()
		} else {
			if runGrader && !graderConfigured() {
				j.appendStderr("grader skipped: CLAUDE_GRADER_API_KEY not set")
			}
			j.mu.Lock()
			j.Status = "ok"
			j.EndedAt = probeDone
			j.mu.Unlock()
		}
	} else {
		j.mu.Lock()
		j.EndedAt = probeDone
		j.mu.Unlock()
	}
}

func (j *evalJob) fail(msg string) {
	j.mu.Lock()
	j.Status = "error"
	j.Err = msg
	j.EndedAt = time.Now()
	j.mu.Unlock()
}

// ---- HTTP handlers ----

type evalStartRequest struct {
	URL       string `json:"url"`
	Key       string `json:"key"`
	Model     string `json:"model"`
	Repeat    int    `json:"repeat"`
	RunGrader *bool  `json:"run_grader,omitempty"`
}

func handleEvalStart(c *gin.Context) {
	var req evalStartRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	req.URL = strings.TrimSpace(req.URL)
	req.Key = strings.TrimSpace(req.Key)
	req.Model = strings.TrimSpace(req.Model)
	if req.URL == "" || req.Key == "" || req.Model == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "url, key, model required"})
		return
	}
	if !strings.HasPrefix(req.URL, "http://") && !strings.HasPrefix(req.URL, "https://") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "url must be http:// or https://"})
		return
	}
	repeat := req.Repeat
	if repeat <= 0 {
		repeat = evalDefaultRepeat
	}
	if repeat > evalMaxRepeat {
		repeat = evalMaxRepeat
	}

	runGrader := graderConfigured()
	if req.RunGrader != nil {
		runGrader = *req.RunGrader && graderConfigured()
	}

	job := &evalJob{
		ID:        newEvalJobID(),
		URL:       req.URL,
		Model:     req.Model,
		Repeat:    repeat,
		Status:    "running",
		StartedAt: time.Now(),
		RunGrader: runGrader,
	}
	evalJobsMu.Lock()
	evalJobs[job.ID] = job
	evalJobsMu.Unlock()

	go runEval(job, req.URL, req.Key, req.Model, repeat)

	c.JSON(http.StatusOK, gin.H{
		"job_id":     job.ID,
		"started_at": job.StartedAt.Unix(),
		"repeat":     repeat,
		"run_grader": runGrader,
	})
}

func handleEvalStatus(c *gin.Context) {
	id := c.Param("id")
	evalJobsMu.Lock()
	job := evalJobs[id]
	evalJobsMu.Unlock()
	if job == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "job not found"})
		return
	}
	s := job.snapshot()
	resp := gin.H{
		"job_id":         id,
		"status":         s.Status,
		"repeat":         s.Repeat,
		"started_at":     s.StartedAt.Unix(),
		"stderr":         s.StderrLog,
		"stderr_trimmed": s.StderrTrim,
	}
	if !s.EndedAt.IsZero() {
		resp["ended_at"] = s.EndedAt.Unix()
		resp["elapsed_ms"] = s.EndedAt.Sub(s.StartedAt).Milliseconds()
	}
	// Trace can be huge while grading is still in flight — return it as
	// soon as probe.mjs finishes so the UI can render it while we wait.
	if s.Status != "running" {
		resp["trace"] = s.Trace
		if s.Err != "" {
			resp["error"] = s.Err
		}
	}
	if s.LLMReport != "" {
		resp["llm_report"] = s.LLMReport
		resp["grader_ms"] = s.GraderMs
	}
	if s.LLMError != "" {
		resp["llm_error"] = s.LLMError
	}
	c.JSON(http.StatusOK, resp)
}

func handleEvalCancel(c *gin.Context) {
	id := c.Param("id")
	evalJobsMu.Lock()
	job := evalJobs[id]
	evalJobsMu.Unlock()
	if job == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "job not found"})
		return
	}
	job.mu.Lock()
	if job.cancel != nil && job.Status == "running" {
		job.cancel()
	}
	job.mu.Unlock()
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// startEvalJobReaper drops finished jobs out of memory after a grace period
// so a long-lived service doesn't accumulate them.
func startEvalJobReaper() {
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			cutoff := time.Now().Add(-evalJobRetention)
			evalJobsMu.Lock()
			for id, j := range evalJobs {
				j.mu.Lock()
				done := j.Status != "running" && !j.EndedAt.IsZero() && j.EndedAt.Before(cutoff)
				j.mu.Unlock()
				if done {
					delete(evalJobs, id)
				}
			}
			evalJobsMu.Unlock()
		}
	}()
}
