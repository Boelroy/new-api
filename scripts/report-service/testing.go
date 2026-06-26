package main

// Unified provider testing: project CRUD + per-project async test runs
// (detect or eval). Run artifacts (trace.md / report.md / stderr.log /
// result.json) live in Cloudflare R2; metadata + status lives in Postgres
// (rs_test_project / rs_test_run). In-memory job map tracks in-flight
// state so UI polling can show live stderr without re-hitting R2.

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	testRunHardTimeout       = 35 * time.Minute
	testJobMemoryGrace       = 10 * time.Minute
	testStderrMemoryMaxBytes = 256 * 1024
	testSignedURLTTL         = 5 * time.Minute
)

// ---- in-memory live state ----

type testRunMem struct {
	ID        string
	StartedAt time.Time
	Status    string // mirrors DB status until terminal
	cancel    context.CancelFunc

	mu         sync.Mutex
	stderrBuf  strings.Builder
	stderrTrim bool
	endedAt    time.Time // non-zero once terminal; used by reaper
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

func (j *testRunMem) markEnded(status string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.Status = status
	j.endedAt = time.Now()
}

// startTestJobReaper drops finished in-memory job state after the grace
// period. DB rows + R2 objects stay forever; this only frees the live
// stderr buffer.
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

// resetRunningTestRuns flips any rs_test_run rows still marked as
// running/grading at boot to status=error, since their in-memory state
// was wiped by the restart.
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
	APIKey    string `json:"api_key,omitempty"` // included on POST response; redacted on list
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
		keys := make([]string, 0, len(runIDs)*4)
		for _, rid := range runIDs {
			keys = append(keys,
				r2RunKey(rid, "trace.md"),
				r2RunKey(rid, "report.md"),
				r2RunKey(rid, "stderr.log"),
				r2RunKey(rid, "result.json"))
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
	ID          string `json:"id"`
	ProjectID   string `json:"project_id"`
	Model       string `json:"model"`
	Kind        string `json:"kind"`
	Status      string `json:"status"`
	PassAt      int    `json:"pass_at"`
	RunGrader   bool   `json:"run_grader"`
	TraceBytes  int64  `json:"trace_bytes"`
	ReportBytes int64  `json:"report_bytes"`
	StderrBytes int64  `json:"stderr_bytes"`
	ResultBytes int64  `json:"result_bytes"`
	ErrorMsg    string `json:"error_msg,omitempty"`
	LLMError    string `json:"llm_error,omitempty"`
	GraderMs    int64  `json:"grader_ms"`
	StartedAt   int64  `json:"started_at"`
	EndedAt     *int64 `json:"ended_at,omitempty"`
	ElapsedMs   *int64 `json:"elapsed_ms,omitempty"`
}

func scanRun(s sqlScanner) (*runRow, error) {
	var r runRow
	var endedAt, elapsedMs sql.NullInt64
	if err := s.Scan(
		&r.ID, &r.ProjectID, &r.Model, &r.Kind, &r.Status, &r.PassAt, &r.RunGrader,
		&r.TraceBytes, &r.ReportBytes, &r.StderrBytes, &r.ResultBytes,
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
	trace_bytes, report_bytes, stderr_bytes, result_bytes,
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
	Kind      string `json:"kind"`
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
	req.Kind = strings.TrimSpace(req.Kind)
	req.Model = strings.TrimSpace(req.Model)
	if req.Kind != "detect" && req.Kind != "eval" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "kind must be 'detect' or 'eval'"})
		return
	}
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
		VALUES ($1, $2, $3, $4, 'running', $5, $6, $7)`,
		runID, pid, req.Model, req.Kind, req.PassAt, runGrader, now); err != nil {
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

	go runTestJob(ctx, mem, proj, req.Kind, req.Model, req.PassAt, runGrader)

	c.JSON(http.StatusOK, gin.H{
		"run_id":     runID,
		"project_id": pid,
		"started_at": now,
		"run_grader": runGrader,
		"kind":       req.Kind,
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
		"trace_bytes": r.TraceBytes, "report_bytes": r.ReportBytes,
		"stderr_bytes": r.StderrBytes, "result_bytes": r.ResultBytes,
		"error_msg": r.ErrorMsg, "llm_error": r.LLMError,
		"grader_ms": r.GraderMs, "started_at": r.StartedAt,
	}
	if r.EndedAt != nil {
		resp["ended_at"] = *r.EndedAt
	}
	if r.ElapsedMs != nil {
		resp["elapsed_ms"] = *r.ElapsedMs
	}
	if r2Configured() {
		ctx := c.Request.Context()
		if r.TraceBytes > 0 {
			if u, err := r2SignedGetURL(ctx, r2RunKey(id, "trace.md"), testSignedURLTTL); err == nil {
				resp["trace_url"] = u
			}
		}
		if r.ReportBytes > 0 {
			if u, err := r2SignedGetURL(ctx, r2RunKey(id, "report.md"), testSignedURLTTL); err == nil {
				resp["report_url"] = u
			}
		}
		if r.StderrBytes > 0 {
			if u, err := r2SignedGetURL(ctx, r2RunKey(id, "stderr.log"), testSignedURLTTL); err == nil {
				resp["stderr_url"] = u
			}
		}
		if r.ResultBytes > 0 {
			if u, err := r2SignedGetURL(ctx, r2RunKey(id, "result.json"), testSignedURLTTL); err == nil {
				resp["result_url"] = u
			}
		}
	}
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
	keys := []string{
		r2RunKey(id, "trace.md"),
		r2RunKey(id, "report.md"),
		r2RunKey(id, "stderr.log"),
		r2RunKey(id, "result.json"),
	}
	_ = r2DeleteObjects(c.Request.Context(), keys)
	if _, err := db.Exec(`DELETE FROM rs_test_run WHERE id = $1`, id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "project_id": r.ProjectID})
}

// ---- background runner ----

func runTestJob(ctx context.Context, mem *testRunMem, proj *projectRow,
	kind, model string, passAt int, runGrader bool) {

	runID := mem.ID
	defer func() {
		// Always upload the final stderr snapshot + write terminal status.
		stderrStr, _ := mem.snapshotStderr()
		stderrBytes := int64(len(stderrStr))
		if stderrBytes > 0 {
			uctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			_ = r2PutObject(uctx, r2RunKey(runID, "stderr.log"), "text/plain; charset=utf-8", []byte(stderrStr))
			cancel()
		}
		endedAt := time.Now().Unix()
		elapsedMs := time.Since(mem.StartedAt).Milliseconds()
		_, _ = db.Exec(`UPDATE rs_test_run
			SET stderr_bytes = $1, ended_at = $2, elapsed_ms = $3
			WHERE id = $4`,
			stderrBytes, endedAt, elapsedMs, runID)
	}()

	var (
		traceMD     string
		probeErr    error
		resultJSON  []byte
	)

	switch kind {
	case "detect":
		opts := detectOptions{
			IntervalMs: detectDefaultIntervalMs,
			MaxRetries: detectDefaultMaxRetries,
		}
		mem.appendStderr("detect: running 6 probes against " + proj.URL)
		res, err := runDetect(ctx, proj.URL, proj.APIKey, model, opts)
		if err != nil {
			probeErr = err
			break
		}
		traceMD = renderDetectTraceMarkdown(res)
		if b, jerr := json.Marshal(res); jerr == nil {
			resultJSON = b
		}
		mem.appendStderr(fmt.Sprintf("detect: classification router=%s/%s backend=%s/%s",
			res.Classification.RouterLabel, res.Classification.RouterConfidence,
			res.Classification.BackendLabel, res.Classification.BackendConfidence))
	case "eval":
		mem.appendStderr(fmt.Sprintf("eval: starting probe.mjs (pass@%d) against %s", passAt, proj.URL))
		tr, err := runEvalProbe(ctx, proj.URL, proj.APIKey, model, passAt, mem.appendStderr)
		traceMD = tr
		probeErr = err
	default:
		probeErr = fmt.Errorf("unknown kind: %s", kind)
	}

	if probeErr != nil {
		mem.appendStderr("probe error: " + probeErr.Error())
		_, _ = db.Exec(`UPDATE rs_test_run SET status='error', error_msg=$1 WHERE id=$2`,
			probeErr.Error(), runID)
		mem.markEnded("error")
		return
	}

	// Upload trace.md (always present on success) and result.json (detect only).
	if traceMD != "" {
		uctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		if err := r2PutObject(uctx, r2RunKey(runID, "trace.md"),
			"text/markdown; charset=utf-8", []byte(traceMD)); err != nil {
			cancel()
			mem.appendStderr("r2 upload trace failed: " + err.Error())
			_, _ = db.Exec(`UPDATE rs_test_run SET status='error', error_msg=$1 WHERE id=$2`,
				"r2 upload trace: "+err.Error(), runID)
			mem.markEnded("error")
			return
		}
		cancel()
		_, _ = db.Exec(`UPDATE rs_test_run SET trace_bytes=$1 WHERE id=$2`,
			int64(len(traceMD)), runID)
	}
	if len(resultJSON) > 0 {
		uctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		if err := r2PutObject(uctx, r2RunKey(runID, "result.json"),
			"application/json; charset=utf-8", resultJSON); err == nil {
			_, _ = db.Exec(`UPDATE rs_test_run SET result_bytes=$1 WHERE id=$2`,
				int64(len(resultJSON)), runID)
		} else {
			mem.appendStderr("r2 upload result.json failed: " + err.Error())
		}
		cancel()
	}

	// Optional grader pass.
	if runGrader && graderConfigured() && traceMD != "" {
		mem.appendStderr("--- probe complete, invoking Claude grader ---")
		_, _ = db.Exec(`UPDATE rs_test_run SET status='grading' WHERE id=$1`, runID)
		mem.mu.Lock()
		mem.Status = "grading"
		mem.mu.Unlock()

		pipelineEnv := "EVAL_PIPELINE_PATH"
		instruction := evalGraderInstruction
		if kind == "detect" {
			pipelineEnv = "DETECT_PIPELINE_PATH"
			instruction = detectGraderInstruction
		}
		pipelineMD, perr := readPipelineFile(pipelineEnv)
		if perr != nil {
			mem.appendStderr("grader: pipeline load failed — " + perr.Error())
			_, _ = db.Exec(`UPDATE rs_test_run SET status='ok', llm_error=$1 WHERE id=$2`,
				"pipeline load: "+perr.Error(), runID)
			mem.markEnded("ok")
			return
		}
		t0 := time.Now()
		report, gerr := runClaudeGrader(context.Background(), instruction, pipelineMD, traceMD)
		elapsed := time.Since(t0).Milliseconds()
		if gerr != nil {
			mem.appendStderr("grader: " + gerr.Error())
			_, _ = db.Exec(`UPDATE rs_test_run SET status='ok', llm_error=$1, grader_ms=$2 WHERE id=$3`,
				gerr.Error(), elapsed, runID)
			mem.markEnded("ok")
			return
		}
		mem.appendStderr(fmt.Sprintf("grader: done in %dms (%d chars)", elapsed, len(report)))
		uctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		if err := r2PutObject(uctx, r2RunKey(runID, "report.md"),
			"text/markdown; charset=utf-8", []byte(report)); err != nil {
			cancel()
			mem.appendStderr("r2 upload report failed: " + err.Error())
			_, _ = db.Exec(`UPDATE rs_test_run SET status='ok', llm_error=$1, grader_ms=$2 WHERE id=$3`,
				"r2 upload report: "+err.Error(), elapsed, runID)
			mem.markEnded("ok")
			return
		}
		cancel()
		_, _ = db.Exec(`UPDATE rs_test_run
			SET status='ok', report_bytes=$1, grader_ms=$2 WHERE id=$3`,
			int64(len(report)), elapsed, runID)
		mem.markEnded("ok")
		return
	}

	_, _ = db.Exec(`UPDATE rs_test_run SET status='ok' WHERE id=$1`, runID)
	mem.markEnded("ok")
}
