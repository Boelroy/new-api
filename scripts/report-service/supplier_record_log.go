package main

// 账号上号 使用日志 — proxies the portal's WEB request-log endpoints
// (/supplier/record-log-list + /supplier/record-log-aggregate, the portal's
// 「使用日志查询」 page) through the shared supplierWeb login.
//
// The portal filters by ONE platform account id — a1_id on platform 1.0, a2_id
// on 2.0 — not by the portal account id we store as remote_account_id, and it
// ignores account_ids lists. Those ids are kept on rs_supplier_account (filled
// by sync, and lazily by refreshSupplierPlatformIDs). The shared login sees
// every account of the supplier, so scoping is enforced here: a supplier names
// one of their own accounts, or none to fan out over all of their own accounts;
// admins may leave the account empty to see everything.

import (
	"cmp"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/lib/pq"
)

// supplierRecordLogQuotaUnit converts the portal's integer quota (row `cost`,
// aggregate `use_quota`) to RMB; the portal UI divides by the same 1e6.
const supplierRecordLogQuotaUnit = 1e6

// supplierRecordLogSlots caps in-flight portal record-log calls across the
// whole service: each call takes ~1.4s and the portal answers 「请求过于频繁」
// once roughly a dozen are in flight (6 sustained 69 calls cleanly on dev).
var supplierRecordLogSlots = make(chan struct{}, 6)

const (
	// supplierRecordLogFanOutWorkers is how many accounts one all-accounts
	// query works on at once; supplierRecordLogSlots is the real cap.
	supplierRecordLogFanOutWorkers = 6
	// supplierRecordLogFanOutMaxRows caps how deep the merged all-accounts log
	// can be paged: page N needs the newest N*page_size rows of every account.
	supplierRecordLogFanOutMaxRows = 1000
)

type supplierRecordLogFilter struct {
	PlatformType string `json:"platform_type"`
	StartTime    string `json:"start_time"`
	EndTime      string `json:"end_time"`
	Provider     string `json:"provider"`
	Model        string `json:"model"`
	AccountID    int64  `json:"account_id"`
	Page         int    `json:"page"`
	PageSize     int    `json:"page_size"`

	// fanOut lists the supplier's own accounts when they picked no account;
	// each is queried separately and the results merged.
	fanOut []supplierScopedAccount
}

type supplierScopedAccount struct {
	ID    int64
	Alias string
}

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

// bindSupplierRecordLogFilter validates the request and enforces ownership.
// On failure it has already written the error response.
func bindSupplierRecordLogFilter(c *gin.Context) (supplierRecordLogFilter, bool) {
	var f supplierRecordLogFilter
	if err := c.ShouldBindJSON(&f); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return f, false
	}
	f.StartTime = strings.TrimSpace(f.StartTime)
	f.EndTime = strings.TrimSpace(f.EndTime)
	f.Provider = strings.TrimSpace(f.Provider)
	f.Model = strings.TrimSpace(f.Model)

	col := supplierPlatformIDColumn(f.PlatformType)
	if col == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "platform_type must be 1.0 or 2.0"})
		return f, false
	}
	if _, err := time.Parse(time.DateTime, f.StartTime); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "start_time must be YYYY-MM-DD HH:MM:SS"})
		return f, false
	}
	// The portal rejects a request without end_time; default to an hour ahead
	// (same as the page default).
	if f.EndTime == "" {
		f.EndTime = time.Now().Add(time.Hour).In(supplierPortalLoc).Format(time.DateTime)
	} else {
		if _, err := time.Parse(time.DateTime, f.EndTime); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "end_time must be YYYY-MM-DD HH:MM:SS"})
			return f, false
		}
	}
	if f.AccountID < 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid account_id"})
		return f, false
	}

	if callerIsSupplierAdmin(c) {
		return f, true
	}
	uidAny, _ := c.Get("user_id")
	uid, _ := uidAny.(int64)
	if f.AccountID == 0 {
		rows, err := db.Query(`SELECT `+col+`, alias FROM rs_supplier_account WHERE uploaded_by=$1 AND `+col+` > 0 ORDER BY id`, uid)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return f, false
		}
		defer rows.Close()
		for rows.Next() {
			var a supplierScopedAccount
			if err := rows.Scan(&a.ID, &a.Alias); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return f, false
			}
			f.fanOut = append(f.fanOut, a)
		}
		if len(f.fanOut) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "没有可查询的账号"})
			return f, false
		}
		return f, true
	}
	var one int
	err := db.QueryRow(`SELECT 1 FROM rs_supplier_account WHERE uploaded_by=$1 AND `+col+`=$2 LIMIT 1`, uid, f.AccountID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		c.JSON(http.StatusForbidden, gin.H{"error": "只能查询自己上传的账号"})
		return f, false
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return f, false
	}
	return f, true
}

// portalPayload builds the portal request body for one account (0 = no account
// filter), sending optional filters only when set (the portal UI does the same).
func (f supplierRecordLogFilter) portalPayload(accountID int64) map[string]any {
	p := map[string]any{"platform_type": f.PlatformType, "start_time": f.StartTime, "end_time": f.EndTime}
	if f.Provider != "" {
		p["provider"] = f.Provider
	}
	if f.Model != "" {
		p["model"] = f.Model
	}
	if accountID > 0 {
		p["account_id"] = accountID
	}
	return p
}

// supplierRecordLogPost POSTs to a portal record-log endpoint and decodes the
// JSON response into out. Throttled responses are retried with backoff.
func supplierRecordLogPost(path string, payload map[string]any, out any) error {
	reqBytes, _ := json.Marshal(payload)
	var (
		status int
		body   []byte
		err    error
	)
	for attempt := range 4 {
		if attempt > 0 {
			time.Sleep(time.Duration(1<<(attempt-1)) * time.Second)
		}
		supplierRecordLogSlots <- struct{}{}
		status, body, err = supplierWeb.proxy(http.MethodPost, "/supplier-account/api/supplier/"+path, reqBytes)
		<-supplierRecordLogSlots
		throttled := err == nil && (status == http.StatusTooManyRequests || strings.Contains(string(body), "频繁"))
		if !throttled {
			break
		}
	}
	if err != nil {
		return err
	}
	if status == http.StatusForbidden {
		return errors.New("号池未开通使用日志查询：" + supplierErr(body))
	}
	if status != http.StatusOK {
		return errors.New(supplierErr(body))
	}
	if err := json.Unmarshal(body, out); err != nil {
		return errors.New("unexpected upstream response")
	}
	return nil
}

// supplierRecordLogFanOut runs fn for every index in [0,n) with bounded
// concurrency and returns the first error.
func supplierRecordLogFanOut(n int, fn func(i int) error) error {
	sem := make(chan struct{}, supplierRecordLogFanOutWorkers)
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		firstErr error
	)
	for i := range n {
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer func() { <-sem; wg.Done() }()
			if err := fn(i); err != nil {
				mu.Lock()
				firstErr = cmp.Or(firstErr, err)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return firstErr
}

type supplierRecordLogPage struct {
	List  []map[string]any `json:"list"`
	Total int64            `json:"total"`
}

func handleSupplierRecordLogList(c *gin.Context) {
	f, ok := bindSupplierRecordLogFilter(c)
	if !ok {
		return
	}
	page := max(f.Page, 1)
	pageSize := f.PageSize
	if pageSize <= 0 || pageSize > 100 {
		pageSize = 50
	}

	var result supplierRecordLogPage
	if len(f.fanOut) == 0 {
		payload := f.portalPayload(f.AccountID)
		payload["start"] = (page - 1) * pageSize
		payload["limit"] = pageSize
		if err := supplierRecordLogPost("record-log-list", payload, &result); err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
			return
		}
	} else {
		// Merge: the newest page*pageSize rows of every account cover the
		// requested page of the combined, time-ordered log.
		need := page * pageSize
		if need > supplierRecordLogFanOutMaxRows {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("全部账号最多查看前 %d 条，请选择单个账号查看更多", supplierRecordLogFanOutMaxRows)})
			return
		}
		perAccount := make([]supplierRecordLogPage, len(f.fanOut))
		err := supplierRecordLogFanOut(len(f.fanOut), func(i int) error {
			acc := &perAccount[i]
			for start := 0; start < need; start += 100 {
				payload := f.portalPayload(f.fanOut[i].ID)
				payload["start"] = start
				payload["limit"] = min(100, need-start)
				var chunk supplierRecordLogPage
				if err := supplierRecordLogPost("record-log-list", payload, &chunk); err != nil {
					return err
				}
				acc.Total = chunk.Total
				acc.List = append(acc.List, chunk.List...)
				if len(chunk.List) < min(100, need-start) {
					break
				}
			}
			return nil
		})
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
			return
		}
		for _, acc := range perAccount {
			result.Total += acc.Total
			result.List = append(result.List, acc.List...)
		}
		// req_time is RFC3339 in one fixed zone, so it sorts as a string.
		slices.SortStableFunc(result.List, func(a, b map[string]any) int {
			ta, _ := a["req_time"].(string)
			tb, _ := b["req_time"].(string)
			return strings.Compare(tb, ta)
		})
		from := min((page-1)*pageSize, len(result.List))
		result.List = result.List[from:min(from+pageSize, len(result.List))]
	}

	isAdmin := callerIsSupplierAdmin(c)
	for _, row := range result.List {
		// Same masking as the account list: the supplier/company name never
		// leaves the server.
		if name, ok := row["account_name"].(string); ok {
			row["account_name"] = maskSupplierAlias(name)
		}
		delete(row, "supplier_name")
		// `other` carries the portal's per-request price breakdown.
		if !isAdmin {
			delete(row, "other")
		}
		if cost, ok := row["cost"].(float64); ok {
			row["cost"] = cost / supplierRecordLogQuotaUnit
		}
	}
	if result.List == nil {
		result.List = []map[string]any{}
	}
	c.JSON(http.StatusOK, gin.H{"list": result.List, "total": result.Total, "page": page, "page_size": pageSize})
}

// supplierRecordLogSum is one aggregate result; use_quota arrives as a decimal
// string, rpm/tpm as numbers.
type supplierRecordLogSum struct {
	UseQuota json.Number `json:"use_quota"`
	RPM      float64     `json:"rpm"`
	TPM      float64     `json:"tpm"`
}

func (s supplierRecordLogSum) costCNY() float64 {
	quota, _ := strconv.ParseFloat(s.UseQuota.String(), 64)
	return quota / supplierRecordLogQuotaUnit
}

type supplierRecordLogAccountSum struct {
	AccountID int64   `json:"account_id"`
	Alias     string  `json:"alias"`
	CostCNY   float64 `json:"cost_cny"`
	RPM       float64 `json:"rpm"`
	TPM       float64 `json:"tpm"`
}

func handleSupplierRecordLogAggregate(c *gin.Context) {
	f, ok := bindSupplierRecordLogFilter(c)
	if !ok {
		return
	}
	metrics := []string{"use_quota", "rpm", "tpm"}

	if len(f.fanOut) == 0 {
		payload := f.portalPayload(f.AccountID)
		payload["metrics"] = metrics
		var sum supplierRecordLogSum
		if err := supplierRecordLogPost("record-log-aggregate", payload, &sum); err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"cost_cny": sum.costCNY(), "rpm": sum.RPM, "tpm": sum.TPM, "fx_rate": supplierFxRate()})
		return
	}

	perAccount := make([]supplierRecordLogAccountSum, len(f.fanOut))
	err := supplierRecordLogFanOut(len(f.fanOut), func(i int) error {
		payload := f.portalPayload(f.fanOut[i].ID)
		payload["metrics"] = metrics
		var sum supplierRecordLogSum
		if err := supplierRecordLogPost("record-log-aggregate", payload, &sum); err != nil {
			return err
		}
		perAccount[i] = supplierRecordLogAccountSum{
			AccountID: f.fanOut[i].ID,
			Alias:     maskSupplierAlias(f.fanOut[i].Alias),
			CostCNY:   sum.costCNY(),
			RPM:       sum.RPM,
			TPM:       sum.TPM,
		}
		return nil
	})
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	var total supplierRecordLogAccountSum
	for _, a := range perAccount {
		total.CostCNY += a.CostCNY
		total.RPM += a.RPM
		total.TPM += a.TPM
	}
	slices.SortStableFunc(perAccount, func(a, b supplierRecordLogAccountSum) int {
		return cmp.Compare(b.CostCNY, a.CostCNY)
	})
	c.JSON(http.StatusOK, gin.H{
		"cost_cny": total.CostCNY,
		"rpm":      total.RPM,
		"tpm":      total.TPM,
		"fx_rate":  supplierFxRate(),
		"accounts": perAccount,
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
