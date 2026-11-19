package main

// 账号上号 使用日志 — proxies the portal's WEB request-log endpoints
// (/supplier/record-log-list + /supplier/record-log-aggregate, the portal's
// 「使用日志查询」 page) through the shared supplierWeb login.
//
// The portal filters by ONE platform account id — a1_id on platform 1.0, a2_id
// on 2.0 — not by the portal account id we store as remote_account_id. Those
// ids are kept on rs_supplier_account (filled by sync, and lazily by
// refreshSupplierPlatformIDs). The shared login sees every account of the
// supplier, so scoping is enforced here: suppliers must name one of their own
// accounts; admins may leave the account empty to see everything.

import (
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"net/http"
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

type supplierRecordLogFilter struct {
	PlatformType string `json:"platform_type"`
	StartTime    string `json:"start_time"`
	EndTime      string `json:"end_time"`
	Provider     string `json:"provider"`
	Model        string `json:"model"`
	AccountID    int64  `json:"account_id"`
	Page         int    `json:"page"`
	PageSize     int    `json:"page_size"`
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
	if f.EndTime != "" {
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
	if f.AccountID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请选择账号"})
		return f, false
	}
	uidAny, _ := c.Get("user_id")
	uid, _ := uidAny.(int64)
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

// portalPayload builds the portal request body, sending optional filters only
// when set (the portal UI does the same).
func (f supplierRecordLogFilter) portalPayload() map[string]any {
	p := map[string]any{"platform_type": f.PlatformType, "start_time": f.StartTime}
	if f.EndTime != "" {
		p["end_time"] = f.EndTime
	}
	if f.Provider != "" {
		p["provider"] = f.Provider
	}
	if f.Model != "" {
		p["model"] = f.Model
	}
	if f.AccountID > 0 {
		p["account_id"] = f.AccountID
	}
	return p
}

// supplierRecordLogCall POSTs to a portal record-log endpoint and returns the
// body, writing the error response itself on failure.
func supplierRecordLogCall(c *gin.Context, path string, payload map[string]any) ([]byte, bool) {
	reqBytes, _ := json.Marshal(payload)
	status, body, err := supplierWeb.proxy(http.MethodPost, "/supplier-account/api/supplier/"+path, reqBytes)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return nil, false
	}
	if status == http.StatusForbidden {
		c.JSON(http.StatusBadGateway, gin.H{"error": "号池未开通使用日志查询：" + supplierErr(body)})
		return nil, false
	}
	if status != http.StatusOK {
		c.JSON(http.StatusBadGateway, gin.H{"error": supplierErr(body)})
		return nil, false
	}
	return body, true
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
	payload := f.portalPayload()
	payload["start"] = (page - 1) * pageSize
	payload["limit"] = pageSize

	body, ok := supplierRecordLogCall(c, "record-log-list", payload)
	if !ok {
		return
	}
	var parsed struct {
		List  []map[string]any `json:"list"`
		Total int64            `json:"total"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "unexpected upstream response"})
		return
	}
	isAdmin := callerIsSupplierAdmin(c)
	for _, row := range parsed.List {
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
	if parsed.List == nil {
		parsed.List = []map[string]any{}
	}
	c.JSON(http.StatusOK, gin.H{"list": parsed.List, "total": parsed.Total, "page": page, "page_size": pageSize})
}

func handleSupplierRecordLogAggregate(c *gin.Context) {
	f, ok := bindSupplierRecordLogFilter(c)
	if !ok {
		return
	}
	payload := f.portalPayload()
	payload["metrics"] = []string{"use_quota", "rpm", "tpm"}
	body, ok := supplierRecordLogCall(c, "record-log-aggregate", payload)
	if !ok {
		return
	}
	// use_quota arrives as a decimal string, rpm/tpm as numbers.
	var parsed struct {
		UseQuota json.Number `json:"use_quota"`
		RPM      float64     `json:"rpm"`
		TPM      float64     `json:"tpm"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "unexpected upstream response"})
		return
	}
	quota, _ := strconv.ParseFloat(parsed.UseQuota.String(), 64)
	c.JSON(http.StatusOK, gin.H{
		"cost_cny": quota / supplierRecordLogQuotaUnit,
		"rpm":      parsed.RPM,
		"tpm":      parsed.TPM,
		"fx_rate":  supplierFxRate(),
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
