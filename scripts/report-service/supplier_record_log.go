package main

// 账号上号 使用日志 — the portal's 「使用日志查询」 request log, mirrored locally.
//
// The portal (WEB /supplier/record-log-list) filters by ONE platform account id
// and ignores id lists, and each call takes ~1.4s, so querying a supplier's
// accounts live does not scale. Instead a leader loop pulls the whole log of
// the shared supplier login every minute (both platforms, no account filter)
// into rs_supplier_record_log, and queries run against the local copy:
//
//   - account ids are platform ids (a1_id on 1.0, a2_id on 2.0), kept on
//     rs_supplier_account (filled by sync, and lazily by
//     refreshSupplierPlatformIDs);
//   - suppliers may pick any subset of their own accounts (none = all of
//     them); admins any account, or none for the whole log;
//   - detail rows are kept supplierRecordLogRetentionDays; per (account, model,
//     hour) totals in rs_supplier_record_log_hourly are kept for good and
//     serve aggregate queries that start before the retention cutoff.

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"log"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/lib/pq"
)

// supplierRecordLogQuotaUnit converts the portal's quota (row `cost`) to RMB;
// the portal UI divides by the same 1e6.
const supplierRecordLogQuotaUnit = 1e6

const (
	supplierRecordLogRetentionDays = 30
	// supplierRecordLogSlice bounds one pull window so a backfill or a
	// catch-up after downtime proceeds in small steps (~3k rows at current
	// traffic) instead of paging one huge window.
	supplierRecordLogSlice = 10 * time.Minute
	// supplierRecordLogOverlap re-reads the tail of the previous window; rows
	// can land a little after their req_time and the portal pages by offset.
	supplierRecordLogOverlap = 2 * time.Minute
	// supplierRecordLogLag keeps the cursor this far behind now for the same
	// reason, so the newest minute is always read again next tick.
	supplierRecordLogLag      = time.Minute
	supplierRecordLogPageSize = 100
)

var supplierRecordLogPlatforms = []string{"2.0", "1.0"}

// supplierPlatformIDColumn maps a portal platform type to the
// rs_supplier_account column holding that platform's account id.
func supplierPlatformIDColumn(platform string) string {
	switch platform {
	case "1.0":
		return "a1_id"
	case "2.0":
		return "a2_id"
	}
	return ""
}

func supplierRecordLogCursorKey(platform string) string {
	return "supplier_record_log_cursor_" + platform
}

// supplierRecordLogCursor is the unix time up to which the platform's log has
// been pulled (0 = never).
func supplierRecordLogCursor(platform string) int64 {
	v, _ := strconv.ParseInt(supplierConfigGet(supplierRecordLogCursorKey(platform)), 10, 64)
	return v
}

// ---- Sync loop ----

func startSupplierRecordLogSync() {
	if supplierAccountBaseURL == "" {
		return
	}
	go func() {
		time.Sleep(60 * time.Second)
		var lastPrune time.Time
		for {
			behind := false
			if IsLeader() && supplierWeb.configured() {
				for _, platform := range supplierRecordLogPlatforms {
					more, err := syncSupplierRecordLogSlice(platform)
					if err != nil {
						log.Printf("[supplier-record-log] sync %s: %v", platform, err)
						continue
					}
					behind = behind || more
				}
				if time.Since(lastPrune) > 6*time.Hour {
					pruneSupplierRecordLog()
					lastPrune = time.Now()
				}
			}
			if behind {
				time.Sleep(time.Second)
				continue
			}
			time.Sleep(time.Minute)
		}
	}()
}

// supplierRecordLogPortalRow is one row of the portal record-log list.
type supplierRecordLogPortalRow struct {
	ID               int64           `json:"id"`
	ReqTime          string          `json:"req_time"`
	AccountID        int64           `json:"account_id"`
	AccountName      string          `json:"account_name"`
	ProviderName     string          `json:"provider_name"`
	ModelName        string          `json:"model_name"`
	Stream           int             `json:"stream"`
	PromptTokens     int64           `json:"prompt_tokens"`
	CompletionTokens int64           `json:"completion_tokens"`
	Cost             float64         `json:"cost"`
	DurationMS       int64           `json:"duration_ms"`
	StatusCode       int             `json:"status_code"`
	ErrCode          string          `json:"err_code"`
	ErrMsg           string          `json:"err_msg"`
	Caller           string          `json:"caller"`
	Other            json.RawMessage `json:"other"`
	// BillingDetail is where platform 1.0 puts the price breakdown.
	BillingDetail json.RawMessage `json:"billing_detail"`
	CreateTime    string          `json:"create_time"`
}

// key returns the row's portal id, or for platform 1.0 — whose rows all carry
// id 0 — a stable hash of the fields that identify one request (both times
// have millisecond precision).
func (r supplierRecordLogPortalRow) key() int64 {
	if r.ID != 0 {
		return r.ID
	}
	h := fnv.New64a()
	fmt.Fprintf(h, "%d|%s|%s|%s|%s", r.AccountID, r.ReqTime, r.CreateTime, r.ModelName, r.Caller)
	return int64(h.Sum64() & math.MaxInt64)
}

// syncSupplierRecordLogSlice pulls one window of a platform's log and advances
// its cursor. Returns true while the cursor is still more than a slice behind
// (backfill / catch-up), so the loop continues without waiting.
func syncSupplierRecordLogSlice(platform string) (bool, error) {
	now := time.Now()
	cursor := supplierRecordLogCursor(platform)
	if cursor == 0 {
		// First run: start from the beginning of yesterday (portal time).
		y := now.In(supplierPortalLoc).AddDate(0, 0, -1)
		cursor = time.Date(y.Year(), y.Month(), y.Day(), 0, 0, 0, 0, supplierPortalLoc).Unix()
	}
	from := time.Unix(cursor, 0).Add(-supplierRecordLogOverlap)
	to := time.Unix(min(cursor+int64(supplierRecordLogSlice/time.Second), now.Unix()), 0)

	if err := pullSupplierRecordLogWindow(platform, from.Unix(), to.Unix()); err != nil {
		return false, err
	}
	if err := rebuildSupplierRecordLogHourly(platform, from.Unix(), to.Unix()); err != nil {
		return false, err
	}

	next := min(to.Unix(), now.Add(-supplierRecordLogLag).Unix())
	if next > cursor {
		if err := supplierConfigSet(supplierRecordLogCursorKey(platform), strconv.FormatInt(next, 10)); err != nil {
			return false, err
		}
	}
	return now.Unix()-next > int64(supplierRecordLogSlice/time.Second), nil
}

// supplierRecordLogSubWindowRows is the target row count of one sub-window;
// below the portal's 100-row page so a window rarely needs a second split.
const supplierRecordLogSubWindowRows = 80

// fetchSupplierRecordLogPage reads one page of a [from, to] window (unix
// seconds, both ends inclusive in portal time) and the window's total.
func fetchSupplierRecordLogPage(platform string, from, to int64, start int) ([]supplierRecordLogPortalRow, int, error) {
	reqBytes, _ := json.Marshal(map[string]any{
		"platform_type": platform,
		"start_time":    time.Unix(from, 0).In(supplierPortalLoc).Format(time.DateTime),
		"end_time":      time.Unix(to, 0).In(supplierPortalLoc).Format(time.DateTime),
		"start":         start,
		"limit":         supplierRecordLogPageSize,
	})
	status, body, err := supplierWeb.proxy(http.MethodPost, "/supplier-account/api/supplier/record-log-list", reqBytes)
	if err != nil {
		return nil, 0, err
	}
	if status != http.StatusOK {
		return nil, 0, errors.New(supplierErr(body))
	}
	var page struct {
		List  []supplierRecordLogPortalRow `json:"list"`
		Total int                          `json:"total"`
	}
	if err := json.Unmarshal(body, &page); err != nil {
		return nil, 0, fmt.Errorf("unexpected upstream response: %w", err)
	}
	return page.List, page.Total, nil
}

// pullSupplierRecordLogWindow stores every row of [from, to]. Offset paging is
// unreliable on the portal (rows sharing a second reorder between pages, so
// deep paging skips some), so a window that does not fit one page is split
// into sub-windows of ~supplierRecordLogSubWindowRows rows, recursively. Only
// a single second with more rows than a page falls back to paging.
func pullSupplierRecordLogWindow(platform string, from, to int64) error {
	rows, total, err := fetchSupplierRecordLogPage(platform, from, to, 0)
	if err != nil {
		return err
	}
	if total <= len(rows) {
		return upsertSupplierRecordLogRows(platform, rows)
	}
	if to <= from {
		for start := len(rows); len(rows) > 0; start += len(rows) {
			if err := upsertSupplierRecordLogRows(platform, rows); err != nil {
				return err
			}
			if start >= total {
				return nil
			}
			if rows, _, err = fetchSupplierRecordLogPage(platform, from, to, start); err != nil {
				return err
			}
		}
		return nil
	}
	parts := int64(min((total+supplierRecordLogSubWindowRows-1)/supplierRecordLogSubWindowRows, int(to-from+1)))
	parts = max(parts, 2)
	step := (to - from + 1 + parts - 1) / parts
	for lo := from; lo <= to; lo += step {
		// Both bounds are inclusive at second granularity, so adjacent
		// sub-windows tile the window exactly.
		if err := pullSupplierRecordLogWindow(platform, lo, min(lo+step-1, to)); err != nil {
			return err
		}
	}
	return nil
}

func upsertSupplierRecordLogRows(platform string, rows []supplierRecordLogPortalRow) error {
	// The portal can repeat an id within one page (seen on 1.0), and one
	// upsert statement may not touch a row twice: keep the last copy.
	seen := make(map[int64]int, len(rows))
	deduped := rows[:0:0]
	for _, r := range rows {
		r.ID = r.key()
		if i, ok := seen[r.ID]; ok {
			deduped[i] = r
			continue
		}
		seen[r.ID] = len(deduped)
		deduped = append(deduped, r)
	}
	rows = deduped
	if len(rows) == 0 {
		return nil
	}
	const cols = 17
	var sb strings.Builder
	sb.WriteString(`INSERT INTO rs_supplier_record_log
	  (platform, id, req_time, account_id, account_name, provider, model, stream,
	   prompt_tokens, completion_tokens, cost, duration_ms, status_code, err_code,
	   err_msg, caller, other) VALUES `)
	args := make([]any, 0, len(rows)*cols)
	for i, r := range rows {
		reqTime, err := time.Parse(time.RFC3339, r.ReqTime)
		if err != nil {
			return fmt.Errorf("row %d: bad req_time %q", r.ID, r.ReqTime)
		}
		other := ""
		for _, raw := range []json.RawMessage{r.Other, r.BillingDetail} {
			if len(raw) > 0 && string(raw) != "null" {
				other = string(raw)
				break
			}
		}
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString("(")
		for j := range cols {
			if j > 0 {
				sb.WriteString(",")
			}
			fmt.Fprintf(&sb, "$%d", i*cols+j+1)
		}
		sb.WriteString(")")
		args = append(args, platform, r.ID, reqTime.Unix(), r.AccountID, r.AccountName,
			r.ProviderName, r.ModelName, r.Stream != 0, r.PromptTokens, r.CompletionTokens,
			r.Cost, r.DurationMS, r.StatusCode, r.ErrCode, r.ErrMsg, r.Caller, other)
	}
	// Rows are re-read by the overlap; refresh the mutable fields in case the
	// portal finalized usage after the first read.
	sb.WriteString(` ON CONFLICT (platform, id) DO UPDATE SET
	   prompt_tokens=EXCLUDED.prompt_tokens, completion_tokens=EXCLUDED.completion_tokens,
	   cost=EXCLUDED.cost, duration_ms=EXCLUDED.duration_ms, status_code=EXCLUDED.status_code,
	   err_code=EXCLUDED.err_code, err_msg=EXCLUDED.err_msg, other=EXCLUDED.other`)
	_, err := db.Exec(sb.String(), args...)
	return err
}

// supplierRecordLogFailedSQL marks a failed request the way the portal UI does:
// an error message, or a non-200 status.
const supplierRecordLogFailedSQL = `(err_msg <> '' OR (status_code > 0 AND status_code <> 200))`

// rebuildSupplierRecordLogHourly recomputes the hourly totals of every hour the
// window [from, to] touches from the detail rows.
func rebuildSupplierRecordLogHourly(platform string, from, to int64) error {
	hourFrom := from / 3600 * 3600
	hourTo := to / 3600 * 3600
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM rs_supplier_record_log_hourly WHERE platform=$1 AND hour BETWEEN $2 AND $3`,
		platform, hourFrom, hourTo); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO rs_supplier_record_log_hourly
	    (platform, account_id, model, hour, requests, failed, prompt_tokens, completion_tokens, cost)
	  SELECT platform, account_id, model, req_time / 3600 * 3600, COUNT(*),
	         COUNT(*) FILTER (WHERE `+supplierRecordLogFailedSQL+`),
	         SUM(prompt_tokens), SUM(completion_tokens), SUM(cost)
	    FROM rs_supplier_record_log
	   WHERE platform=$1 AND req_time >= $2 AND req_time < $3
	   GROUP BY platform, account_id, model, req_time / 3600 * 3600`,
		platform, hourFrom, hourTo+3600); err != nil {
		return err
	}
	return tx.Commit()
}

func pruneSupplierRecordLog() {
	cutoff := time.Now().AddDate(0, 0, -supplierRecordLogRetentionDays).Unix()
	res, err := db.Exec(`DELETE FROM rs_supplier_record_log WHERE req_time < $1`, cutoff)
	if err != nil {
		log.Printf("[supplier-record-log] prune: %v", err)
		return
	}
	if n, _ := res.RowsAffected(); n > 0 {
		log.Printf("[supplier-record-log] pruned %d rows older than %d days", n, supplierRecordLogRetentionDays)
	}
}

// ---- Queries (local) ----

type supplierRecordLogQuery struct {
	PlatformType string  `json:"platform_type"`
	StartTime    string  `json:"start_time"`
	EndTime      string  `json:"end_time"`
	Model        string  `json:"model"`
	AccountIDs   []int64 `json:"account_ids"`
	Page         int     `json:"page"`
	PageSize     int     `json:"page_size"`

	start, end int64
	// scoped means AccountIDs restricts the query (always for suppliers).
	scoped bool
}

// bindSupplierRecordLogQuery validates the request and resolves the account
// scope. On failure it has already written the error response.
func bindSupplierRecordLogQuery(c *gin.Context) (supplierRecordLogQuery, bool) {
	var q supplierRecordLogQuery
	if err := c.ShouldBindJSON(&q); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return q, false
	}
	col := supplierPlatformIDColumn(q.PlatformType)
	if col == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "platform_type must be 1.0 or 2.0"})
		return q, false
	}
	start, err := time.ParseInLocation(time.DateTime, strings.TrimSpace(q.StartTime), supplierPortalLoc)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "start_time must be YYYY-MM-DD HH:MM:SS"})
		return q, false
	}
	end := time.Now().Add(time.Hour)
	if s := strings.TrimSpace(q.EndTime); s != "" {
		if end, err = time.ParseInLocation(time.DateTime, s, supplierPortalLoc); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "end_time must be YYYY-MM-DD HH:MM:SS"})
			return q, false
		}
	}
	q.start, q.end = start.Unix(), end.Unix()
	q.Model = strings.TrimSpace(q.Model)
	q.AccountIDs = slices.DeleteFunc(q.AccountIDs, func(id int64) bool { return id <= 0 })

	if callerIsSupplierAdmin(c) {
		q.scoped = len(q.AccountIDs) > 0
		return q, true
	}
	uidAny, _ := c.Get("user_id")
	uid, _ := uidAny.(int64)
	rows, err := db.Query(`SELECT `+col+` FROM rs_supplier_account WHERE uploaded_by=$1 AND `+col+` > 0`, uid)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return q, false
	}
	defer rows.Close()
	own := make([]int64, 0)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return q, false
		}
		own = append(own, id)
	}
	if len(own) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "没有可查询的账号"})
		return q, false
	}
	for _, id := range q.AccountIDs {
		if !slices.Contains(own, id) {
			c.JSON(http.StatusForbidden, gin.H{"error": "只能查询自己上传的账号"})
			return q, false
		}
	}
	if len(q.AccountIDs) == 0 {
		q.AccountIDs = own
	}
	q.scoped = true
	return q, true
}

// where builds the shared filter over table columns platform / account_id /
// model plus a time column, returning the clause and its args.
func (q supplierRecordLogQuery) where(timeCol string, from, to int64) (string, []any) {
	clause := `platform=$1 AND ` + timeCol + ` >= $2 AND ` + timeCol + ` <= $3`
	args := []any{q.PlatformType, from, to}
	if q.scoped {
		args = append(args, pq.Array(q.AccountIDs))
		clause += fmt.Sprintf(` AND account_id = ANY($%d)`, len(args))
	}
	if q.Model != "" {
		args = append(args, q.Model)
		clause += fmt.Sprintf(` AND model = $%d`, len(args))
	}
	return clause, args
}

func handleSupplierRecordLogList(c *gin.Context) {
	q, ok := bindSupplierRecordLogQuery(c)
	if !ok {
		return
	}
	page := max(q.Page, 1)
	pageSize := q.PageSize
	if pageSize <= 0 || pageSize > 100 {
		pageSize = 50
	}
	where, args := q.where("req_time", q.start, q.end)

	var total int64
	if err := db.QueryRow(`SELECT COUNT(*) FROM rs_supplier_record_log WHERE `+where, args...).Scan(&total); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	args = append(args, pageSize, (page-1)*pageSize)
	rows, err := db.Query(fmt.Sprintf(`SELECT id, req_time, account_id, account_name, provider, model, stream,
	        prompt_tokens, completion_tokens, cost, duration_ms, status_code, err_code, err_msg, other
	   FROM rs_supplier_record_log WHERE %s
	  ORDER BY req_time DESC, id DESC LIMIT $%d OFFSET $%d`, where, len(args)-1, len(args)), args...)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()

	isAdmin := callerIsSupplierAdmin(c)
	list := make([]gin.H, 0, pageSize)
	for rows.Next() {
		var (
			id, reqTime, accountID, prompt, completion, durationMS int64
			accountName, provider, model, errCode, errMsg, other   string
			stream                                                 bool
			cost                                                   float64
			statusCode                                             int
		)
		if err := rows.Scan(&id, &reqTime, &accountID, &accountName, &provider, &model, &stream,
			&prompt, &completion, &cost, &durationMS, &statusCode, &errCode, &errMsg, &other); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		row := gin.H{
			"id":                id,
			"req_time":          time.Unix(reqTime, 0).In(supplierPortalLoc).Format(time.RFC3339),
			"account_id":        accountID,
			"account_name":      maskSupplierAlias(accountName),
			"provider_name":     provider,
			"model_name":        model,
			"stream":            stream,
			"prompt_tokens":     prompt,
			"completion_tokens": completion,
			"cost":              cost / supplierRecordLogQuotaUnit,
			"duration_ms":       durationMS,
			"status_code":       statusCode,
			"err_code":          errCode,
			"err_msg":           errMsg,
		}
		// `other` carries the portal's per-request price breakdown.
		if isAdmin && other != "" {
			row["other"] = json.RawMessage(other)
		}
		list = append(list, row)
	}
	c.JSON(http.StatusOK, gin.H{"list": list, "total": total, "page": page, "page_size": pageSize})
}

type supplierRecordLogAccountSum struct {
	AccountID int64   `json:"account_id"`
	Alias     string  `json:"alias"`
	Requests  int64   `json:"requests"`
	Failed    int64   `json:"failed"`
	CostCNY   float64 `json:"cost_cny"`
}

func handleSupplierRecordLogAggregate(c *gin.Context) {
	q, ok := bindSupplierRecordLogQuery(c)
	if !ok {
		return
	}

	// Detail rows cover the retention window; older starts read the hourly
	// totals (hour granularity).
	var sumSQL string
	var args []any
	if q.start >= time.Now().AddDate(0, 0, -supplierRecordLogRetentionDays).Unix() {
		where, a := q.where("req_time", q.start, q.end)
		sumSQL = `SELECT account_id, COUNT(*), COUNT(*) FILTER (WHERE ` + supplierRecordLogFailedSQL + `), COALESCE(SUM(cost), 0)
		            FROM rs_supplier_record_log WHERE ` + where + ` GROUP BY account_id`
		args = a
	} else {
		where, a := q.where("hour", q.start/3600*3600, q.end)
		sumSQL = `SELECT account_id, SUM(requests), SUM(failed), SUM(cost)
		            FROM rs_supplier_record_log_hourly WHERE ` + where + ` GROUP BY account_id`
		args = a
	}
	rows, err := db.Query(sumSQL, args...)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()
	accounts := make([]supplierRecordLogAccountSum, 0)
	var total supplierRecordLogAccountSum
	for rows.Next() {
		var a supplierRecordLogAccountSum
		var cost float64
		if err := rows.Scan(&a.AccountID, &a.Requests, &a.Failed, &cost); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		a.CostCNY = cost / supplierRecordLogQuotaUnit
		total.Requests += a.Requests
		total.Failed += a.Failed
		total.CostCNY += a.CostCNY
		accounts = append(accounts, a)
	}
	if err := rows.Err(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Label accounts with their (masked) local alias, falling back to the
	// portal's account name for accounts that are not in the local table.
	col := supplierPlatformIDColumn(q.PlatformType)
	ids := make([]int64, len(accounts))
	for i, a := range accounts {
		ids[i] = a.AccountID
	}
	aliases := make(map[int64]string, len(ids))
	if aliasRows, err := db.Query(`SELECT `+col+`, alias FROM rs_supplier_account WHERE `+col+` = ANY($1)`, pq.Array(ids)); err == nil {
		for aliasRows.Next() {
			var id int64
			var alias string
			if aliasRows.Scan(&id, &alias) == nil {
				aliases[id] = alias
			}
		}
		aliasRows.Close()
	}
	for i := range accounts {
		if alias, ok := aliases[accounts[i].AccountID]; ok {
			accounts[i].Alias = maskSupplierAlias(alias)
		}
	}
	slices.SortStableFunc(accounts, func(a, b supplierRecordLogAccountSum) int {
		return cmp.Compare(b.CostCNY, a.CostCNY)
	})

	// RPM / TPM over the last synced minute (the copy trails real time by the
	// sync lag, so "now" would read low).
	syncedTo := supplierRecordLogCursor(q.PlatformType)
	var rpm, tpm int64
	if syncedTo > 0 {
		where, a := q.where("req_time", syncedTo-60, syncedTo)
		if err := db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(prompt_tokens + completion_tokens), 0)
		                         FROM rs_supplier_record_log WHERE `+where, a...).Scan(&rpm, &tpm); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"cost_cny":  total.CostCNY,
		"requests":  total.Requests,
		"failed":    total.Failed,
		"rpm":       rpm,
		"tpm":       tpm,
		"fx_rate":   supplierFxRate(),
		"synced_to": syncedTo,
		"accounts":  accounts,
	})
}

type supplierRecordLogAccount struct {
	ID       int64  `json:"id"`
	Provider string `json:"provider"`
	Alias    string `json:"alias"`
	Remark   string `json:"remark"`
	A1ID     int64  `json:"a1_id"`
	A2ID     int64  `json:"a2_id"`
	Username string `json:"username,omitempty"`
}

// handleSupplierRecordLogAccounts lists the accounts the caller may pick in the
// usage-log filter, with their platform account ids. Accounts still missing
// both ids (fresh uploads) trigger a throttled refresh from the portal roster.
func handleSupplierRecordLogAccounts(c *gin.Context) {
	isAdmin := callerIsSupplierAdmin(c)
	uidAny, _ := c.Get("user_id")
	uid, _ := uidAny.(int64)

	load := func() ([]supplierRecordLogAccount, bool, error) {
		query := `SELECT a.id, a.provider, a.alias, a.remark, a.a1_id, a.a2_id, COALESCE(u.username, '')
		            FROM rs_supplier_account a
		            LEFT JOIN rs_auth_user u ON u.id = a.uploaded_by`
		args := []any{}
		if !isAdmin {
			query += ` WHERE a.uploaded_by=$1`
			args = append(args, uid)
		}
		rows, err := db.Query(query+` ORDER BY a.id DESC`, args...)
		if err != nil {
			return nil, false, err
		}
		defer rows.Close()
		out := make([]supplierRecordLogAccount, 0)
		missing := false
		for rows.Next() {
			var a supplierRecordLogAccount
			if err := rows.Scan(&a.ID, &a.Provider, &a.Alias, &a.Remark, &a.A1ID, &a.A2ID, &a.Username); err != nil {
				return nil, false, err
			}
			if a.A1ID == 0 && a.A2ID == 0 {
				missing = true
			}
			if !isAdmin {
				a.Username = ""
			}
			a.Alias = maskSupplierAlias(a.Alias)
			out = append(out, a)
		}
		return out, missing, rows.Err()
	}

	accounts, missing, err := load()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if missing && supplierWeb.configured() {
		if refreshed, err := refreshSupplierPlatformIDs(); err != nil {
			log.Printf("[supplier] platform id refresh failed: %v", err)
		} else if refreshed {
			if accounts, _, err = load(); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
		}
	}
	c.JSON(http.StatusOK, gin.H{"accounts": accounts})
}

// supplierPlatformIDRefresh throttles roster pulls: the portal is shared with
// human users and the roster is paged, so at most one pull per minute.
var supplierPlatformIDRefresh struct {
	mu   sync.Mutex
	last time.Time
}

// refreshSupplierPlatformIDs copies a1_id / a2_id from the portal roster onto
// the local rows. Returns false without calling the portal when a pull ran in
// the last minute.
func refreshSupplierPlatformIDs() (bool, error) {
	supplierPlatformIDRefresh.mu.Lock()
	defer supplierPlatformIDRefresh.mu.Unlock()
	if time.Since(supplierPlatformIDRefresh.last) < time.Minute {
		return false, nil
	}
	supplierPlatformIDRefresh.last = time.Now()

	raws, err := fetchPortalAccountsRaw(supplierWeb, "")
	if err != nil {
		return false, err
	}
	ids := make([]int64, 0, len(raws))
	a1 := make([]int64, 0, len(raws))
	a2 := make([]int64, 0, len(raws))
	for _, raw := range raws {
		var a struct {
			ID   int64 `json:"id"`
			A1ID int64 `json:"a1_id"`
			A2ID int64 `json:"a2_id"`
		}
		if json.Unmarshal(raw, &a) != nil || a.ID == 0 {
			continue
		}
		ids = append(ids, a.ID)
		a1 = append(a1, a.A1ID)
		a2 = append(a2, a.A2ID)
	}
	if len(ids) == 0 {
		return true, nil
	}
	_, err = db.Exec(
		`UPDATE rs_supplier_account a SET a1_id=v.a1, a2_id=v.a2
		   FROM unnest($1::bigint[], $2::bigint[], $3::bigint[]) AS v(id, a1, a2)
		  WHERE a.remote_account_id = v.id AND (a.a1_id <> v.a1 OR a.a2_id <> v.a2)`,
		pq.Array(ids), pq.Array(a1), pq.Array(a2),
	)
	return err == nil, err
}
