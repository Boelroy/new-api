package main

// Unified provider testing: project CRUD + per-project async test runs.
// Each click runs BOTH detect and eval back-to-back as a single combined
// run. Artifacts (detect/{trace,report,result} + eval/{trace,report} +
// stderr.log) live in Cloudflare R2; metadata + status lives in Postgres
// (rs_test_project / rs_test_run). In-memory job map tracks in-flight
// state so UI polling can show live stderr without re-hitting R2. The
// /api/testing/runs/:id/file proxy avoids browser↔R2 CORS.

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/gin-gonic/gin"
)

const (
	testRunHardTimeout       = 35 * time.Minute
	testJobMemoryGrace       = 10 * time.Minute
	testStderrMemoryMaxBytes = 256 * 1024
)

// Artifact kinds streamed through the file proxy.
var allowedFileKinds = map[string]struct{}{
	"detect-trace":  {},
	"detect-report": {},
	"detect-result": {},
	"eval-trace":    {},
	"eval-report":   {},
	"stderr":        {},
}

func r2KeyForArtifact(runID, kind string) (string, string) {
	switch kind {
	case "detect-trace":
		return fmt.Sprintf("runs/%s/detect/trace.md", runID), "text/markdown; charset=utf-8"
	case "detect-report":
		return fmt.Sprintf("runs/%s/detect/report.md", runID), "text/markdown; charset=utf-8"
	case "detect-result":
		return fmt.Sprintf("runs/%s/detect/result.json", runID), "application/json; charset=utf-8"
	case "eval-trace":
		return fmt.Sprintf("runs/%s/eval/trace.md", runID), "text/markdown; charset=utf-8"
	case "eval-report":
		return fmt.Sprintf("runs/%s/eval/report.md", runID), "text/markdown; charset=utf-8"
	case "stderr":
		return fmt.Sprintf("runs/%s/stderr.log", runID), "text/plain; charset=utf-8"
	}
	return "", ""
}

// ---- in-memory live state ----

type testRunMem struct {
	ID        string
	StartedAt time.Time
	Status    string
	cancel    context.CancelFunc

	mu         sync.Mutex
	stderrBuf  strings.Builder
	stderrTrim bool
	endedAt    time.Time
}

var (
	testJobsMu sync.Mutex
	testJobs   = map[string]*testRunMem{}
)

func (j *testRunMem) appendStderr(line string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.stderrBuf.Len()+len(line)+1 > testStderrMemoryMaxBytes {
		s := j.stderrBuf.String()
		cut := len(s) / 4
		if cut < len(s) {
			j.stderrBuf.Reset()
			j.stderrBuf.WriteString(s[cut:])
			j.stderrTrim = true
		}
	}
	j.stderrBuf.WriteString(line)
	j.stderrBuf.WriteByte('\n')
}

func (j *testRunMem) snapshotStderr() (string, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.stderrBuf.String(), j.stderrTrim
}

func (j *testRunMem) setStatus(s string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.Status = s
}

func (j *testRunMem) markEnded(status string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.Status = status
	j.endedAt = time.Now()
}

func startTestJobReaper() {
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			cutoff := time.Now().Add(-testJobMemoryGrace)
			testJobsMu.Lock()
			for id, j := range testJobs {
				j.mu.Lock()
				done := !j.endedAt.IsZero() && j.endedAt.Before(cutoff)
				j.mu.Unlock()
				if done {
					delete(testJobs, id)
				}
			}
			testJobsMu.Unlock()
		}
	}()
}

func resetRunningTestRuns() {
	if db == nil {
		return
	}
	now := time.Now().Unix()
	_, _ = db.Exec(`UPDATE rs_test_run
		SET status = 'error',
		    error_msg = COALESCE(NULLIF(error_msg, ''), 'service restarted'),
		    ended_at = $1,
		    elapsed_ms = COALESCE(elapsed_ms, ($1 - started_at) * 1000)
		WHERE status IN ('running', 'grading')`, now)
}

func newRunID() string {
	buf := make([]byte, 8)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}

// ---- project handlers ----

type projectRow struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	URL       string `json:"url"`
	APIKey    string `json:"api_key,omitempty"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
	RunCount  int64  `json:"run_count,omitempty"`
}

func maskAPIKey(k string) string {
	if len(k) <= 8 {
		return strings.Repeat("*", len(k))
	}
	return k[:4] + strings.Repeat("*", 4) + k[len(k)-4:]
}

func handleTestingProjectsList(c *gin.Context) {
	rows, err := db.Query(`SELECT p.id, p.name, p.url, p.api_key, p.created_at, p.updated_at,
		COALESCE((SELECT COUNT(*) FROM rs_test_run r WHERE r.project_id = p.id), 0)
		FROM rs_test_project p
		ORDER BY p.created_at DESC`)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()
	out := make([]projectRow, 0)
	for rows.Next() {
		var p projectRow
		if err := rows.Scan(&p.ID, &p.Name, &p.URL, &p.APIKey, &p.CreatedAt, &p.UpdatedAt, &p.RunCount); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		p.APIKey = maskAPIKey(p.APIKey)
		out = append(out, p)
	}
	c.JSON(http.StatusOK, gin.H{"projects": out})
}

type projectUpsertRequest struct {
	Name   string `json:"name"`
	URL    string `json:"url"`
	APIKey string `json:"api_key"`
}

func handleTestingProjectCreate(c *gin.Context) {
	var req projectUpsertRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	req.URL = strings.TrimSpace(req.URL)
	req.APIKey = strings.TrimSpace(req.APIKey)
	if req.Name == "" || req.URL == "" || req.APIKey == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name, url, api_key required"})
		return
	}
	if !strings.HasPrefix(req.URL, "http://") && !strings.HasPrefix(req.URL, "https://") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "url must be http:// or https://"})
		return
	}
	id := newRunID()
	now := time.Now().Unix()
	if _, err := db.Exec(`INSERT INTO rs_test_project (id, name, url, api_key, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $5)`, id, req.Name, req.URL, req.APIKey, now); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, projectRow{
		ID: id, Name: req.Name, URL: req.URL, APIKey: maskAPIKey(req.APIKey),
		CreatedAt: now, UpdatedAt: now,
	})
}

func loadProject(id string) (*projectRow, error) {
	var p projectRow
	err := db.QueryRow(`SELECT id, name, url, api_key, created_at, updated_at
		FROM rs_test_project WHERE id = $1`, id).
		Scan(&p.ID, &p.Name, &p.URL, &p.APIKey, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func handleTestingProjectGet(c *gin.Context) {
	id := c.Param("id")
	p, err := loadProject(id)
	if err == sql.ErrNoRows {
		c.JSON(http.StatusNotFound, gin.H{"error": "project not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	p.APIKey = maskAPIKey(p.APIKey)
	c.JSON(http.StatusOK, p)
}

func handleTestingProjectUpdate(c *gin.Context) {
	id := c.Param("id")
	var req projectUpsertRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	req.URL = strings.TrimSpace(req.URL)
	req.APIKey = strings.TrimSpace(req.APIKey)
	if req.Name == "" && req.URL == "" && req.APIKey == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "at least one of name/url/api_key required"})
		return
	}
	cur, err := loadProject(id)
	if err == sql.ErrNoRows {
		c.JSON(http.StatusNotFound, gin.H{"error": "project not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if req.Name != "" {
		cur.Name = req.Name
	}
	if req.URL != "" {
		if !strings.HasPrefix(req.URL, "http://") && !strings.HasPrefix(req.URL, "https://") {
			c.JSON(http.StatusBadRequest, gin.H{"error": "url must be http:// or https://"})
			return
		}
		cur.URL = req.URL
	}
	if req.APIKey != "" {
		cur.APIKey = req.APIKey
	}
	cur.UpdatedAt = time.Now().Unix()
	if _, err := db.Exec(`UPDATE rs_test_project SET name=$1, url=$2, api_key=$3, updated_at=$4
		WHERE id=$5`, cur.Name, cur.URL, cur.APIKey, cur.UpdatedAt, id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	cur.APIKey = maskAPIKey(cur.APIKey)
	c.JSON(http.StatusOK, cur)
}

func handleTestingProjectDelete(c *gin.Context) {
	id := c.Param("id")
	if _, err := loadProject(id); err == sql.ErrNoRows {
		c.JSON(http.StatusNotFound, gin.H{"error": "project not found"})
		return
	} else if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	rows, err := db.Query(`SELECT id FROM rs_test_run WHERE project_id = $1`, id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	var runIDs []string
	for rows.Next() {
		var r string
		if err := rows.Scan(&r); err == nil {
			runIDs = append(runIDs, r)
		}
	}
	rows.Close()
	if len(runIDs) > 0 {
		keys := make([]string, 0, len(runIDs)*6)
		for _, rid := range runIDs {
			for k := range allowedFileKinds {
				key, _ := r2KeyForArtifact(rid, k)
				if key != "" {
					keys = append(keys, key)
				}
			}
		}
		_ = r2DeleteObjects(c.Request.Context(), keys)
	}
	if _, err := db.Exec(`DELETE FROM rs_test_project WHERE id = $1`, id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "deleted_runs": len(runIDs)})
}

// ---- run handlers ----

type runRow struct {
	ID                string `json:"id"`
	ProjectID         string `json:"project_id"`
	Model             string `json:"model"`
	Kind              string `json:"kind"`
	Status            string `json:"status"`
	PassAt            int    `json:"pass_at"`
	RunGrader         bool   `json:"run_grader"`
	DetectTraceBytes  int64  `json:"detect_trace_bytes"`
	DetectReportBytes int64  `json:"detect_report_bytes"`
	DetectResultBytes int64  `json:"detect_result_bytes"`
	EvalTraceBytes    int64  `json:"eval_trace_bytes"`
	EvalReportBytes   int64  `json:"eval_report_bytes"`
	StderrBytes       int64  `json:"stderr_bytes"`
	ErrorMsg          string `json:"error_msg,omitempty"`
	LLMError          string `json:"llm_error,omitempty"`
	GraderMs          int64  `json:"grader_ms"`
	StartedAt         int64  `json:"started_at"`
	EndedAt           *int64 `json:"ended_at,omitempty"`
	ElapsedMs         *int64 `json:"elapsed_ms,omitempty"`
}

func scanRun(s sqlScanner) (*runRow, error) {
	var r runRow
	var endedAt, elapsedMs sql.NullInt64
	if err := s.Scan(
		&r.ID, &r.ProjectID, &r.Model, &r.Kind, &r.Status, &r.PassAt, &r.RunGrader,
		&r.DetectTraceBytes, &r.DetectReportBytes, &r.DetectResultBytes,
		&r.EvalTraceBytes, &r.EvalReportBytes, &r.StderrBytes,
		&r.ErrorMsg, &r.LLMError, &r.GraderMs, &r.StartedAt, &endedAt, &elapsedMs,
	); err != nil {
		return nil, err
	}
	if endedAt.Valid {
		v := endedAt.Int64
		r.EndedAt = &v
	}
	if elapsedMs.Valid {
		v := elapsedMs.Int64
		r.ElapsedMs = &v
	}
	return &r, nil
}

type sqlScanner interface {
	Scan(dest ...any) error
}

const runSelectCols = `id, project_id, model, kind, status, pass_at, run_grader,
	detect_trace_bytes, detect_report_bytes, detect_result_bytes,
	eval_trace_bytes, eval_report_bytes, stderr_bytes,
	error_msg, llm_error, grader_ms, started_at, ended_at, elapsed_ms`

func handleTestingRunList(c *gin.Context) {
	pid := c.Param("id")
	if _, err := loadProject(pid); err == sql.ErrNoRows {
		c.JSON(http.StatusNotFound, gin.H{"error": "project not found"})
		return
	} else if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	rows, err := db.Query(`SELECT `+runSelectCols+`
		FROM rs_test_run WHERE project_id = $1
		ORDER BY started_at DESC`, pid)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()
	out := make([]*runRow, 0)
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		out = append(out, r)
	}
	c.JSON(http.StatusOK, gin.H{"runs": out})
}

type runStartRequest struct {
	Model     string `json:"model"`
	PassAt    int    `json:"pass_at"`
	RunGrader *bool  `json:"run_grader,omitempty"`
}

func handleTestingRunStart(c *gin.Context) {
	if !r2Configured() {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "R2 not configured"})
		return
	}
	pid := c.Param("id")
	proj, err := loadProject(pid)
	if err == sql.ErrNoRows {
		c.JSON(http.StatusNotFound, gin.H{"error": "project not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	var req runStartRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	req.Model = strings.TrimSpace(req.Model)
	if req.Model == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "model required"})
		return
	}
	if req.PassAt <= 0 {
		req.PassAt = 1
	}
	if req.PassAt > evalMaxRepeat {
		req.PassAt = evalMaxRepeat
	}
	runGrader := graderConfigured()
	if req.RunGrader != nil {
		runGrader = *req.RunGrader && graderConfigured()
	}

	runID := newRunID()
	now := time.Now().Unix()
	if _, err := db.Exec(`INSERT INTO rs_test_run
		(id, project_id, model, kind, status, pass_at, run_grader, started_at)
		VALUES ($1, $2, $3, 'combined', 'running', $4, $5, $6)`,
		runID, pid, req.Model, req.PassAt, runGrader, now); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), testRunHardTimeout)
	mem := &testRunMem{
		ID:        runID,
		StartedAt: time.Now(),
		Status:    "running",
		cancel:    cancel,
	}
	testJobsMu.Lock()
	testJobs[runID] = mem
	testJobsMu.Unlock()

	go runCombinedTestJob(ctx, mem, proj, req.Model, req.PassAt, runGrader)

	c.JSON(http.StatusOK, gin.H{
		"run_id":     runID,
		"project_id": pid,
		"started_at": now,
		"run_grader": runGrader,
		"model":      req.Model,
		"pass_at":    req.PassAt,
	})
}

func loadRun(id string) (*runRow, error) {
	row := db.QueryRow(`SELECT `+runSelectCols+` FROM rs_test_run WHERE id = $1`, id)
	return scanRun(row)
}

func handleTestingRunDetail(c *gin.Context) {
	id := c.Param("id")
	r, err := loadRun(id)
	if err == sql.ErrNoRows {
		c.JSON(http.StatusNotFound, gin.H{"error": "run not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	resp := gin.H{
		"id": r.ID, "project_id": r.ProjectID, "model": r.Model, "kind": r.Kind,
		"status": r.Status, "pass_at": r.PassAt, "run_grader": r.RunGrader,
		"detect_trace_bytes":  r.DetectTraceBytes,
		"detect_report_bytes": r.DetectReportBytes,
		"detect_result_bytes": r.DetectResultBytes,
		"eval_trace_bytes":    r.EvalTraceBytes,
		"eval_report_bytes":   r.EvalReportBytes,
		"stderr_bytes":        r.StderrBytes,
		"error_msg":           r.ErrorMsg,
		"llm_error":           r.LLMError,
		"grader_ms":           r.GraderMs,
		"started_at":          r.StartedAt,
	}
	if r.EndedAt != nil {
		resp["ended_at"] = *r.EndedAt
	}
	if r.ElapsedMs != nil {
		resp["elapsed_ms"] = *r.ElapsedMs
	}
	// File proxy URLs (server-relative; avoid R2 CORS).
	files := map[string]int64{
		"detect-trace":  r.DetectTraceBytes,
		"detect-report": r.DetectReportBytes,
		"detect-result": r.DetectResultBytes,
		"eval-trace":    r.EvalTraceBytes,
		"eval-report":   r.EvalReportBytes,
		"stderr":        r.StderrBytes,
	}
	urls := gin.H{}
	for kind, n := range files {
		if n > 0 {
			urls[kind] = fmt.Sprintf("/api/testing/runs/%s/file?kind=%s", id, kind)
		}
	}
	resp["files"] = urls
	c.JSON(http.StatusOK, resp)
}

func handleTestingRunStatus(c *gin.Context) {
	id := c.Param("id")
	r, err := loadRun(id)
	if err == sql.ErrNoRows {
		c.JSON(http.StatusNotFound, gin.H{"error": "run not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	resp := gin.H{
		"id": r.ID, "status": r.Status, "started_at": r.StartedAt,
	}
	if r.EndedAt != nil {
		resp["ended_at"] = *r.EndedAt
	}
	if r.ElapsedMs != nil {
		resp["elapsed_ms"] = *r.ElapsedMs
	}
	if r.ErrorMsg != "" {
		resp["error_msg"] = r.ErrorMsg
	}
	testJobsMu.Lock()
	mem := testJobs[id]
	testJobsMu.Unlock()
	if mem != nil {
		buf, trim := mem.snapshotStderr()
		resp["stderr"] = buf
		resp["stderr_trimmed"] = trim
	}
	c.JSON(http.StatusOK, resp)
}

func handleTestingRunCancel(c *gin.Context) {
	id := c.Param("id")
	testJobsMu.Lock()
	mem := testJobs[id]
	testJobsMu.Unlock()
	if mem == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "run not in flight"})
		return
	}
	if mem.cancel != nil {
		mem.cancel()
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func handleTestingRunDelete(c *gin.Context) {
	id := c.Param("id")
	r, err := loadRun(id)
	if err == sql.ErrNoRows {
		c.JSON(http.StatusNotFound, gin.H{"error": "run not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	keys := make([]string, 0, len(allowedFileKinds))
	for k := range allowedFileKinds {
		key, _ := r2KeyForArtifact(id, k)
		if key != "" {
			keys = append(keys, key)
		}
	}
	_ = r2DeleteObjects(c.Request.Context(), keys)
	if _, err := db.Exec(`DELETE FROM rs_test_run WHERE id = $1`, id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "project_id": r.ProjectID})
}

// handleTestingRunFile streams an R2 object back to the browser. Avoids
// the CORS hop the browser would otherwise need to do against R2.
func handleTestingRunFile(c *gin.Context) {
	id := c.Param("id")
	kind := strings.TrimSpace(c.Query("kind"))
	if _, ok := allowedFileKinds[kind]; !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unknown kind"})
		return
	}
	if _, err := loadRun(id); err == sql.ErrNoRows {
		c.JSON(http.StatusNotFound, gin.H{"error": "run not found"})
		return
	}
	key, contentType := r2KeyForArtifact(id, kind)
	if key == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unknown kind"})
		return
	}
	if err := r2InitOnce(); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": err.Error()})
		return
	}
	out, err := r2Client.GetObject(c.Request.Context(), &s3.GetObjectInput{
		Bucket: aws.String(r2BucketName),
		Key:    aws.String(key),
	})
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	defer out.Body.Close()
	c.Header("Content-Type", contentType)
	if out.ContentLength != nil {
		c.Header("Content-Length", fmt.Sprintf("%d", *out.ContentLength))
	}
	// Cache aggressively — artifacts are immutable per run.
	c.Header("Cache-Control", "private, max-age=300")
	c.Status(http.StatusOK)
	_, _ = io.Copy(c.Writer, out.Body)
}

// ---- background runner ----

// runCombinedTestJob runs detect first, then eval, both for the same model
// against the same project. Each phase's artifacts go under a per-kind
// subpath in R2 so we can serve them independently.
func runCombinedTestJob(ctx context.Context, mem *testRunMem, proj *projectRow,
	model string, passAt int, runGrader bool) {

	runID := mem.ID

	defer func() {
		stderrStr, _ := mem.snapshotStderr()
		stderrBytes := int64(len(stderrStr))
		if stderrBytes > 0 {
			key, ct := r2KeyForArtifact(runID, "stderr")
			uctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			_ = r2PutObject(uctx, key, ct, []byte(stderrStr))
			cancel()
		}
		endedAt := time.Now().Unix()
		elapsedMs := time.Since(mem.StartedAt).Milliseconds()
		_, _ = db.Exec(`UPDATE rs_test_run
			SET stderr_bytes = $1, ended_at = $2, elapsed_ms = $3
			WHERE id = $4`,
			stderrBytes, endedAt, elapsedMs, runID)
	}()

	totalGraderMs := int64(0)
	var llmErrors []string

	// ---- Phase 1: Detect ----
	mem.appendStderr("=== detect: 6 probes against " + proj.URL + " ===")
	mem.setStatus("running")
	dRes, dErr := runDetect(ctx, proj.URL, proj.APIKey, model, detectOptions{
		IntervalMs: detectDefaultIntervalMs,
		MaxRetries: detectDefaultMaxRetries,
	})
	if dErr != nil {
		mem.appendStderr("detect error: " + dErr.Error())
		_, _ = db.Exec(`UPDATE rs_test_run SET status='error', error_msg=$1 WHERE id=$2`,
			"detect: "+dErr.Error(), runID)
		mem.markEnded("error")
		return
	}
	mem.appendStderr(fmt.Sprintf("detect: classification router=%s/%s backend=%s/%s",
		dRes.Classification.RouterLabel, dRes.Classification.RouterConfidence,
		dRes.Classification.BackendLabel, dRes.Classification.BackendConfidence))

	detectTraceMD := renderDetectTraceMarkdown(dRes)
	detectResultJSON, _ := json.Marshal(dRes)

	if detectTraceMD != "" {
		key, ct := r2KeyForArtifact(runID, "detect-trace")
		uctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		if err := r2PutObject(uctx, key, ct, []byte(detectTraceMD)); err == nil {
			_, _ = db.Exec(`UPDATE rs_test_run SET detect_trace_bytes=$1 WHERE id=$2`,
				int64(len(detectTraceMD)), runID)
		} else {
			mem.appendStderr("r2 upload detect-trace failed: " + err.Error())
		}
		cancel()
	}
	if len(detectResultJSON) > 0 {
		key, ct := r2KeyForArtifact(runID, "detect-result")
		uctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		if err := r2PutObject(uctx, key, ct, detectResultJSON); err == nil {
			_, _ = db.Exec(`UPDATE rs_test_run SET detect_result_bytes=$1 WHERE id=$2`,
				int64(len(detectResultJSON)), runID)
		} else {
			mem.appendStderr("r2 upload detect-result failed: " + err.Error())
		}
		cancel()
	}

	if runGrader && graderConfigured() && detectTraceMD != "" {
		mem.appendStderr("--- detect probe complete, invoking Claude grader for detect ---")
		_, _ = db.Exec(`UPDATE rs_test_run SET status='grading' WHERE id=$1`, runID)
		mem.setStatus("grading")
		pipelineMD, perr := readPipelineFile("DETECT_PIPELINE_PATH")
		if perr != nil {
			mem.appendStderr("grader detect: pipeline load failed — " + perr.Error())
			llmErrors = append(llmErrors, "detect pipeline: "+perr.Error())
		} else {
			t0 := time.Now()
			report, gerr := runClaudeGrader(context.Background(), detectGraderInstruction, pipelineMD, detectTraceMD)
			elapsed := time.Since(t0).Milliseconds()
			totalGraderMs += elapsed
			if gerr != nil {
				mem.appendStderr("grader detect: " + gerr.Error())
				llmErrors = append(llmErrors, "detect grader: "+gerr.Error())
			} else {
				mem.appendStderr(fmt.Sprintf("grader detect: done in %dms (%d chars)", elapsed, len(report)))
				key, ct := r2KeyForArtifact(runID, "detect-report")
				uctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
				if err := r2PutObject(uctx, key, ct, []byte(report)); err == nil {
					_, _ = db.Exec(`UPDATE rs_test_run SET detect_report_bytes=$1 WHERE id=$2`,
						int64(len(report)), runID)
				} else {
					mem.appendStderr("r2 upload detect-report failed: " + err.Error())
					llmErrors = append(llmErrors, "detect upload: "+err.Error())
				}
				cancel()
			}
		}
	}

	// ---- Phase 2: Eval ----
	if ctx.Err() != nil {
		mem.appendStderr("cancelled before eval phase")
		_, _ = db.Exec(`UPDATE rs_test_run SET status='cancelled', error_msg='cancelled before eval' WHERE id=$1`, runID)
		mem.markEnded("cancelled")
		return
	}
	mem.appendStderr(fmt.Sprintf("=== eval: probe.mjs (pass@%d) against %s ===", passAt, proj.URL))
	_, _ = db.Exec(`UPDATE rs_test_run SET status='running' WHERE id=$1`, runID)
	mem.setStatus("running")
	evalTraceMD, eErr := runEvalProbe(ctx, proj.URL, proj.APIKey, model, passAt, mem.appendStderr)
	if eErr != nil {
		mem.appendStderr("eval error: " + eErr.Error())
		// Don't fail the whole run — detect already succeeded. Note the error and continue.
		if evalTraceMD == "" {
			finalErr := "eval: " + eErr.Error()
			llmErr := ""
			if len(llmErrors) > 0 {
				llmErr = strings.Join(llmErrors, " ; ")
			}
			_, _ = db.Exec(`UPDATE rs_test_run SET status='error', error_msg=$1, llm_error=$2, grader_ms=$3 WHERE id=$4`,
				finalErr, llmErr, totalGraderMs, runID)
			mem.markEnded("error")
			return
		}
	}

	if evalTraceMD != "" {
		key, ct := r2KeyForArtifact(runID, "eval-trace")
		uctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		if err := r2PutObject(uctx, key, ct, []byte(evalTraceMD)); err == nil {
			_, _ = db.Exec(`UPDATE rs_test_run SET eval_trace_bytes=$1 WHERE id=$2`,
				int64(len(evalTraceMD)), runID)
		} else {
			mem.appendStderr("r2 upload eval-trace failed: " + err.Error())
		}
		cancel()
	}

	if runGrader && graderConfigured() && evalTraceMD != "" {
		mem.appendStderr("--- eval probe complete, invoking Claude grader for eval ---")
		_, _ = db.Exec(`UPDATE rs_test_run SET status='grading' WHERE id=$1`, runID)
		mem.setStatus("grading")
		pipelineMD, perr := readPipelineFile("EVAL_PIPELINE_PATH")
		if perr != nil {
			mem.appendStderr("grader eval: pipeline load failed — " + perr.Error())
			llmErrors = append(llmErrors, "eval pipeline: "+perr.Error())
		} else {
			t0 := time.Now()
			report, gerr := runClaudeGrader(context.Background(), evalGraderInstruction, pipelineMD, evalTraceMD)
			elapsed := time.Since(t0).Milliseconds()
			totalGraderMs += elapsed
			if gerr != nil {
				mem.appendStderr("grader eval: " + gerr.Error())
				llmErrors = append(llmErrors, "eval grader: "+gerr.Error())
			} else {
				mem.appendStderr(fmt.Sprintf("grader eval: done in %dms (%d chars)", elapsed, len(report)))
				key, ct := r2KeyForArtifact(runID, "eval-report")
				uctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
				if err := r2PutObject(uctx, key, ct, []byte(report)); err == nil {
					_, _ = db.Exec(`UPDATE rs_test_run SET eval_report_bytes=$1 WHERE id=$2`,
						int64(len(report)), runID)
				} else {
					mem.appendStderr("r2 upload eval-report failed: " + err.Error())
					llmErrors = append(llmErrors, "eval upload: "+err.Error())
				}
				cancel()
			}
		}
	}

	// Done.
	finalErr := ""
	if eErr != nil {
		finalErr = "eval (partial): " + eErr.Error()
	}
	llmErr := ""
	if len(llmErrors) > 0 {
		llmErr = strings.Join(llmErrors, " ; ")
	}
	_, _ = db.Exec(`UPDATE rs_test_run SET status='ok', error_msg=$1, llm_error=$2, grader_ms=$3 WHERE id=$4`,
		finalErr, llmErr, totalGraderMs, runID)
	mem.markEnded("ok")
}
