package main

// Daily usage exports for the independent supplier-billing service. Both
// return Beijing business days and a "through" date: days after it are not
// complete yet and must not be treated as settled zero usage.

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
)

var billingExportZone = time.FixedZone("Asia/Shanghai", 8*60*60)

func billingExportRange(c *gin.Context) (time.Time, time.Time, bool) {
	start, err1 := time.ParseInLocation("2006-01-02", c.Query("start"), billingExportZone)
	end, err2 := time.ParseInLocation("2006-01-02", c.Query("end"), billingExportZone)
	if err1 != nil || err2 != nil || end.Before(start) || end.Sub(start) > 92*24*time.Hour {
		c.JSON(http.StatusBadRequest, gin.H{"error": "start and end must be dates within 93 days"})
		return time.Time{}, time.Time{}, false
	}
	return start, end, true
}

// handleBillingRemoteTagUsage reports each Beijing day's used-quota growth of
// a remote profile's channels, summed by channel tag. Every snapshot capture
// holds all channels, so a day is the difference between the last captures at
// or before its two midnights. Channels deleted mid-day lose that day's usage;
// channels no longer in the mirror are reported under "(deleted)".
func handleBillingRemoteTagUsage(c *gin.Context) {
	profileID, err := strconv.ParseInt(c.Query("profile_id"), 10, 64)
	if err != nil || profileID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "profile_id is required"})
		return
	}
	start, end, ok := billingExportRange(c)
	if !ok {
		return
	}
	var latest int64
	if err := db.QueryRow(`SELECT COALESCE(MAX(captured_at), 0) FROM remote_channel_snapshot WHERE profile_id = $1`, profileID).Scan(&latest); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	rows, err := db.Query(`
		WITH bounds AS (
		  SELECT d::date AS day,
		         (SELECT MAX(captured_at) FROM remote_channel_snapshot
		           WHERE profile_id = $1 AND captured_at <= EXTRACT(EPOCH FROM d)::bigint) AS cap_from,
		         (SELECT MAX(captured_at) FROM remote_channel_snapshot
		           WHERE profile_id = $1 AND captured_at <= EXTRACT(EPOCH FROM d + interval '1 day')::bigint) AS cap_to
		    FROM generate_series($2::timestamptz, $3::timestamptz, interval '1 day') d
		)
		SELECT to_char(b.day, 'YYYY-MM-DD'),
		       COALESCE(NULLIF(TRIM(cc.tag), ''), CASE WHEN cc.remote_channel_id IS NULL THEN '(deleted)' ELSE '(untagged)' END),
		       SUM(GREATEST(t.used_quota - COALESCE(f.used_quota, 0), 0))::bigint,
		       COUNT(*) FILTER (WHERE t.used_quota > COALESCE(f.used_quota, 0))
		  FROM bounds b
		  JOIN remote_channel_snapshot t ON t.profile_id = $1 AND t.captured_at = b.cap_to
		  LEFT JOIN remote_channel_snapshot f ON f.profile_id = $1 AND f.captured_at = b.cap_from AND f.remote_channel_id = t.remote_channel_id
		  LEFT JOIN remote_channel_current cc ON cc.profile_id = $1 AND cc.remote_channel_id = t.remote_channel_id
		 WHERE b.cap_from IS NOT NULL
		 GROUP BY 1, 2
		 ORDER BY 1, 2`,
		profileID, start.Format(time.RFC3339), end.Format(time.RFC3339))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()
	type row struct {
		Date     string `json:"date"`
		Tag      string `json:"tag"`
		USD      string `json:"usd"`
		Channels int64  `json:"channels"`
	}
	out := make([]row, 0)
	for rows.Next() {
		var r row
		var quota int64
		if err := rows.Scan(&r.Date, &r.Tag, &quota, &r.Channels); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if quota == 0 {
			continue
		}
		// 500,000 quota = 1 USD; six decimals keep the conversion exact enough.
		r.USD = fmt.Sprintf("%d.%06d", quota/500000, (quota%500000)*2)
		out = append(out, r)
	}
	// A day is complete once a capture exists at or after its closing midnight.
	through := ""
	for day := start; !day.After(end); day = day.AddDate(0, 0, 1) {
		if day.AddDate(0, 0, 1).Unix() > latest {
			break
		}
		through = day.Format("2006-01-02")
	}
	c.JSON(http.StatusOK, gin.H{"rows": out, "through": through, "latest_capture": latest})
}

// handleBillingCCMaxUsage reports CC Max portal list-price bills per uploader
// and day, in CNY and in USD at the supplier FX rate used by the CC Max pages.
// Bills are pulled daily up to yesterday (ccmax_bill_synced_through).
func handleBillingCCMaxUsage(c *gin.Context) {
	start, end, ok := billingExportRange(c)
	if !ok {
		return
	}
	rows, err := db.Query(`
		SELECT to_char(b.ds, 'YYYY-MM-DD'), u.id, u.username, COUNT(DISTINCT b.remote_account_id), SUM(b.cost_micro)::bigint
		  FROM rs_ccmax_daily_bill b
		  JOIN rs_ccmax_account a ON a.remote_account_id = b.remote_account_id
		  JOIN rs_auth_user u ON u.id = a.uploaded_by
		 WHERE b.ds BETWEEN $1::date AND $2::date
		 GROUP BY 1, 2, 3
		HAVING SUM(b.cost_micro) > 0
		 ORDER BY 1, 3`,
		start.Format("2006-01-02"), end.Format("2006-01-02"))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()
	type row struct {
		Date     string `json:"date"`
		UserID   int64  `json:"user_id"`
		Username string `json:"username"`
		Accounts int64  `json:"accounts"`
		CNY      string `json:"cny"`
		USD      string `json:"usd"`
	}
	fx := supplierFxRate()
	out := make([]row, 0)
	for rows.Next() {
		var r row
		var micro int64
		if err := rows.Scan(&r.Date, &r.UserID, &r.Username, &r.Accounts, &micro); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		r.CNY = fmt.Sprintf("%d.%06d", micro/1_000_000, micro%1_000_000)
		r.USD = fmt.Sprintf("%.6f", float64(micro)/1e6/fx)
		out = append(out, r)
	}
	c.JSON(http.StatusOK, gin.H{"rows": out, "through": supplierConfigGet(cfgCCMaxBillSyncedThrough), "fx_rate": fx})
}
