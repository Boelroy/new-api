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
	ID           string `json:"id"`
	Name         string `json:"name"`
	URL          string `json:"url"`
	APIKey       string `json:"api_key,omitempty"`
	GraderURL    string `json:"grader_url"`
	GraderAPIKey string `json:"grader_api_key,omitempty"`
	GraderModel  string `json:"grader_model"`
	CreatedAt    int64  `json:"created_at"`
	UpdatedAt    int64  `json:"updated_at"`
	RunCount     int64  `json:"run_count,omitempty"`
}

func maskAPIKey(k string) string {
	if len(k) <= 8 {
		return strings.Repeat("*", len(k))
	}
	return k[:4] + strings.Repeat("*", 4) + k[len(k)-4:]
}

func handleTestingProjectsList(c *gin.Context) {
	rows, err := db.Query(`SELECT p.id, p.name, p.url, p.api_key,
		p.grader_url, p.grader_api_key, p.grader_model,
		p.created_at, p.updated_at,
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
		if err := rows.Scan(&p.ID, &p.Name, &p.URL, &p.APIKey,
			&p.GraderURL, &p.GraderAPIKey, &p.GraderModel,
			&p.CreatedAt, &p.UpdatedAt, &p.RunCount); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		p.APIKey = maskAPIKey(p.APIKey)
		p.GraderAPIKey = maskAPIKey(p.GraderAPIKey)
		out = append(out, p)
	}
	c.JSON(http.StatusOK, gin.H{"projects": out})
}

type projectUpsertRequest struct {
	Name         string `json:"name"`
	URL          string `json:"url"`
	APIKey       string `json:"api_key"`
	GraderURL    string `json:"grader_url"`
	GraderAPIKey string `json:"grader_api_key"`
	GraderModel  string `json:"grader_model"`
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
	req.GraderURL = strings.TrimSpace(req.GraderURL)
	req.GraderAPIKey = strings.TrimSpace(req.GraderAPIKey)
	req.GraderModel = strings.TrimSpace(req.GraderModel)
	if req.Name == "" || req.URL == "" || req.APIKey == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name, url, api_key required"})
		return
	}
	if !strings.HasPrefix(req.URL, "http://") && !strings.HasPrefix(req.URL, "https://") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "url must be http:// or https://"})
		return
	}
	if req.GraderURL != "" && !strings.HasPrefix(req.GraderURL, "http://") && !strings.HasPrefix(req.GraderURL, "https://") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "grader_url must be http:// or https://"})
		return
	}
	id := newRunID()
	now := time.Now().Unix()
	if _, err := db.Exec(`INSERT INTO rs_test_project
		(id, name, url, api_key, grader_url, grader_api_key, grader_model, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $8)`,
		id, req.Name, req.URL, req.APIKey,
		req.GraderURL, req.GraderAPIKey, req.GraderModel, now); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, projectRow{
		ID: id, Name: req.Name, URL: req.URL, APIKey: maskAPIKey(req.APIKey),
		GraderURL: req.GraderURL, GraderAPIKey: maskAPIKey(req.GraderAPIKey), GraderModel: req.GraderModel,
		CreatedAt: now, UpdatedAt: now,
	})
}

func loadProject(id string) (*projectRow, error) {
	var p projectRow
	err := db.QueryRow(`SELECT id, name, url, api_key,
		grader_url, grader_api_key, grader_model,
		created_at, updated_at
		FROM rs_test_project WHERE id = $1`, id).
		Scan(&p.ID, &p.Name, &p.URL, &p.APIKey,
			&p.GraderURL, &p.GraderAPIKey, &p.GraderModel,
			&p.CreatedAt, &p.UpdatedAt)
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
	p.GraderAPIKey = maskAPIKey(p.GraderAPIKey)
	c.JSON(http.StatusOK, p)
}

// projectPatchRequest uses pointer fields so callers can distinguish "leave
// unchanged" (nil) from "set to empty string" (non-nil "") — critical for
// grader_url / grader_model, where the empty string clears the config.
type projectPatchRequest struct {
	Name         *string `json:"name,omitempty"`
	URL          *string `json:"url,omitempty"`
	APIKey       *string `json:"api_key,omitempty"`
	GraderURL    *string `json:"grader_url,omitempty"`
	GraderAPIKey *string `json:"grader_api_key,omitempty"`
	GraderModel  *string `json:"grader_model,omitempty"`
}

func handleTestingProjectUpdate(c *gin.Context) {
	id := c.Param("id")
	var req projectPatchRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
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
	changed := false
	if req.Name != nil {
		if v := strings.TrimSpace(*req.Name); v != "" && v != cur.Name {
			cur.Name = v
			changed = true
		}
	}
	if req.URL != nil {
		if v := strings.TrimSpace(*req.URL); v != "" && v != cur.URL {
			if !strings.HasPrefix(v, "http://") && !strings.HasPrefix(v, "https://") {
				c.JSON(http.StatusBadRequest, gin.H{"error": "url must be http:// or https://"})
				return
			}
			cur.URL = v
			changed = true
		}
	}
	if req.APIKey != nil {
		// Empty string means "keep the existing key" (mirrors legacy behavior).
		if v := strings.TrimSpace(*req.APIKey); v != "" {
			cur.APIKey = v
			changed = true
		}
	}
	if req.GraderURL != nil {
		v := strings.TrimSpace(*req.GraderURL)
		if v != "" && !strings.HasPrefix(v, "http://") && !strings.HasPrefix(v, "https://") {
			c.JSON(http.StatusBadRequest, gin.H{"error": "grader_url must be http:// or https://"})
			return
		}
		if v != cur.GraderURL {
			cur.GraderURL = v
			changed = true
		}
	}
	if req.GraderAPIKey != nil {
		// Non-empty overwrites; empty string clears the stored key. Distinct
		// from api_key semantics because grader creds may need to be removed.
		v := strings.TrimSpace(*req.GraderAPIKey)
		cur.GraderAPIKey = v
		changed = true
	}
	if req.GraderModel != nil {
		v := strings.TrimSpace(*req.GraderModel)
		if v != cur.GraderModel {
			cur.GraderModel = v
			changed = true
		}
	}
	if !changed {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no changes"})
		return
	}
	cur.UpdatedAt = time.Now().Unix()
	if _, err := db.Exec(`UPDATE rs_test_project
		SET name=$1, url=$2, api_key=$3,
		    grader_url=$4, grader_api_key=$5, grader_model=$6,
		    updated_at=$7
		WHERE id=$8`,
		cur.Name, cur.URL, cur.APIKey,
		cur.GraderURL, cur.GraderAPIKey, cur.GraderModel,
		cur.UpdatedAt, id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	cur.APIKey = maskAPIKey(cur.APIKey)
	cur.GraderAPIKey = maskAPIKey(cur.GraderAPIKey)
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
	// Grader only runs when the project has both grader URL + api key set.
	// Callers can opt out via run_grader:false in the request body.
	hasGraderCreds := graderCredsPresent(proj.GraderURL, proj.GraderAPIKey)
	runGrader := hasGraderCreds
	if req.RunGrader != nil {
		runGrader = *req.RunGrader && hasGraderCreds
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

// handleTestingRunRegrade re-runs the Claude grader for one phase
// (detect | eval) using the trace already in R2. The run's status flips
// back to 'grading' while the goroutine works; UI polls /status as usual.
func handleTestingRunRegrade(c *gin.Context) {
	id := c.Param("id")
	phase := strings.TrimSpace(c.Query("phase"))
	if phase == "" {
		var body struct {
			Phase string `json:"phase"`
		}
		_ = c.ShouldBindJSON(&body)
		phase = strings.TrimSpace(body.Phase)
	}
	if phase != "detect" && phase != "eval" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "phase must be 'detect' or 'eval'"})
		return
	}
	r, err := loadRun(id)
	if err == sql.ErrNoRows {
		c.JSON(http.StatusNotFound, gin.H{"error": "run not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	// Grader creds live on the project, so a regrade needs the project
	// row to know where + who to call. Fail fast when creds aren't set.
	proj, perr := loadProject(r.ProjectID)
	if perr == sql.ErrNoRows {
		c.JSON(http.StatusNotFound, gin.H{"error": "project not found"})
		return
	}
	if perr != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": perr.Error()})
		return
	}
	if !graderCredsPresent(proj.GraderURL, proj.GraderAPIKey) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "project has no grader URL / api key configured"})
		return
	}
	var traceBytes int64
	if phase == "detect" {
		traceBytes = r.DetectTraceBytes
	} else {
		traceBytes = r.EvalTraceBytes
	}
	if traceBytes == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": phase + " trace not available — nothing to regrade"})
		return
	}
	// Reject if another job is already in flight on this run.
	testJobsMu.Lock()
	if mem, ok := testJobs[id]; ok {
		mem.mu.Lock()
		isRunning := mem.endedAt.IsZero()
		mem.mu.Unlock()
		if isRunning {
			testJobsMu.Unlock()
			c.JSON(http.StatusConflict, gin.H{"error": "another job is already in flight for this run"})
			return
		}
		delete(testJobs, id)
	}
	testJobsMu.Unlock()

	_, _ = db.Exec(`UPDATE rs_test_run SET status='grading' WHERE id=$1`, id)
	ctx, cancel := context.WithTimeout(context.Background(), testRunHardTimeout)
	mem := &testRunMem{
		ID:        id,
		StartedAt: time.Now(),
		Status:    "grading",
		cancel:    cancel,
	}
	testJobsMu.Lock()
	testJobs[id] = mem
	testJobsMu.Unlock()

	go runGraderRetry(ctx, mem, phase, proj.GraderURL, proj.GraderAPIKey, proj.GraderModel)

	c.JSON(http.StatusOK, gin.H{"ok": true, "phase": phase})
}

func runGraderRetry(ctx context.Context, mem *testRunMem, phase, graderURL, graderAPIKey, graderModel string) {
	runID := mem.ID

	var (
		traceKey    string
		pipelineEnv string
		instruction string
		reportKind  string
		bytesCol    string
	)
	switch phase {
	case "detect":
		traceKey, _ = r2KeyForArtifact(runID, "detect-trace")
		pipelineEnv = "DETECT_PIPELINE_PATH"
		instruction = detectGraderInstruction
		reportKind = "detect-report"
		bytesCol = "detect_report_bytes"
	case "eval":
		traceKey, _ = r2KeyForArtifact(runID, "eval-trace")
		pipelineEnv = "EVAL_PIPELINE_PATH"
		instruction = evalGraderInstruction
		reportKind = "eval-report"
		bytesCol = "eval_report_bytes"
	}

	finish := func(status, llmErr string, reportBytes, elapsed int64) {
		updates := []string{"status=$1"}
		args := []any{status}
		idx := 2
		if llmErr != "" {
			updates = append(updates, fmt.Sprintf("llm_error=$%d", idx))
			args = append(args, llmErr)
			idx++
		} else {
			updates = append(updates, "llm_error=''")
		}
		if reportBytes > 0 {
			updates = append(updates, fmt.Sprintf("%s=$%d", bytesCol, idx))
			args = append(args, reportBytes)
			idx++
		}
		if elapsed > 0 {
			updates = append(updates, fmt.Sprintf("grader_ms = grader_ms + $%d", idx))
			args = append(args, elapsed)
			idx++
		}
		args = append(args, runID)
		query := fmt.Sprintf(`UPDATE rs_test_run SET %s WHERE id=$%d`, strings.Join(updates, ", "), idx)
		_, _ = db.Exec(query, args...)
		mem.markEnded(status)
	}

	mem.appendStderr(fmt.Sprintf("=== manual retry: %s grader ===", phase))

	if err := r2InitOnce(); err != nil {
		mem.appendStderr("r2 init failed: " + err.Error())
		finish("done", phase+" grader retry: r2 init: "+err.Error(), 0, 0)
		return
	}
	out, err := r2Client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(r2BucketName),
		Key:    aws.String(traceKey),
	})
	if err != nil {
		mem.appendStderr("r2 fetch trace failed: " + err.Error())
		finish("done", phase+" grader retry: r2 fetch: "+err.Error(), 0, 0)
		return
	}
	traceBuf, rerr := io.ReadAll(out.Body)
	_ = out.Body.Close()
	if rerr != nil {
		mem.appendStderr("read trace failed: " + rerr.Error())
		finish("done", phase+" grader retry: read trace: "+rerr.Error(), 0, 0)
		return
	}

	pipelineMD, perr := readPipelineFile(pipelineEnv)
	if perr != nil {
		mem.appendStderr("pipeline load: " + perr.Error())
		finish("done", phase+" grader retry: pipeline: "+perr.Error(), 0, 0)
		return
	}

	t0 := time.Now()
	report, gerr := runDirectHTTPGrader(context.Background(),
		graderURL, graderAPIKey, graderModel,
		instruction, pipelineMD, string(traceBuf))
	elapsed := time.Since(t0).Milliseconds()
	if gerr != nil {
		mem.appendStderr("grader: " + gerr.Error())
		finish("done", phase+" grader retry: "+gerr.Error(), 0, elapsed)
		return
	}
	mem.appendStderr(fmt.Sprintf("grader: done in %dms (%d chars)", elapsed, len(report)))

	key, ct := r2KeyForArtifact(runID, reportKind)
	uctx, ucancel := context.WithTimeout(context.Background(), 60*time.Second)
	if err := r2PutObject(uctx, key, ct, []byte(report)); err != nil {
		ucancel()
		mem.appendStderr("upload report failed: " + err.Error())
		finish("done", phase+" grader retry: upload: "+err.Error(), 0, elapsed)
		return
	}
	ucancel()
	finish("done", "", int64(len(report)), elapsed)
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

	if runGrader && graderCredsPresent(proj.GraderURL, proj.GraderAPIKey) && detectTraceMD != "" {
		mem.appendStderr("--- detect probe complete, invoking Claude grader for detect ---")
		_, _ = db.Exec(`UPDATE rs_test_run SET status='grading' WHERE id=$1`, runID)
		mem.setStatus("grading")
		pipelineMD, perr := readPipelineFile("DETECT_PIPELINE_PATH")
		if perr != nil {
			mem.appendStderr("grader detect: pipeline load failed — " + perr.Error())
			llmErrors = append(llmErrors, "detect pipeline: "+perr.Error())
		} else {
			t0 := time.Now()
			report, gerr := runDirectHTTPGrader(context.Background(),
				proj.GraderURL, proj.GraderAPIKey, proj.GraderModel,
				detectGraderInstruction, pipelineMD, detectTraceMD)
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
	evalTraceMD, eErr := runEvalProbe(ctx, proj.URL, proj.APIKey, model, runID, passAt, mem.appendStderr)
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

	if runGrader && graderCredsPresent(proj.GraderURL, proj.GraderAPIKey) && evalTraceMD != "" {
		mem.appendStderr("--- eval probe complete, invoking Claude grader for eval ---")
		_, _ = db.Exec(`UPDATE rs_test_run SET status='grading' WHERE id=$1`, runID)
		mem.setStatus("grading")
		pipelineMD, perr := readPipelineFile("EVAL_PIPELINE_PATH")
		if perr != nil {
			mem.appendStderr("grader eval: pipeline load failed — " + perr.Error())
			llmErrors = append(llmErrors, "eval pipeline: "+perr.Error())
		} else {
			t0 := time.Now()
			report, gerr := runDirectHTTPGrader(context.Background(),
				proj.GraderURL, proj.GraderAPIKey, proj.GraderModel,
				evalGraderInstruction, pipelineMD, evalTraceMD)
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
	_, _ = db.Exec(`UPDATE rs_test_run SET status='done', error_msg=$1, llm_error=$2, grader_ms=$3 WHERE id=$4`,
		finalErr, llmErr, totalGraderMs, runID)
	mem.markEnded("done")
}
