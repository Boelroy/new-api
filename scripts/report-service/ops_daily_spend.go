package main

// 每日消耗: once a day (DailySpendHour, UTC+8) the 运维通知 loop reports the
// previous UTC+8 day's spend in USD —
//
//   - 主站: local new-api usage from report_daily_agg, by studio;
//   - remote: each remote newapi profile's used-quota growth between the
//     snapshot captures at the day's two midnights;
//   - 第三方: 账号上号 portal bills (RMB), CC Max daily bills (RMB, list
//     price) and the KHub overview (USD).
//
// 供给台 only exposes cumulative spend per key, so it has no daily figure.

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	cfgOpsDailySpendDay   = "ops_notify_daily_spend_day"
	opsDailySpendTopLines = 8
)

// fmtUSD renders an amount as $1,234.56.
func fmtUSD(v float64) string {
	s := strconv.FormatFloat(v, 'f', 2, 64)
	intPart, frac, _ := strings.Cut(s, ".")
	neg := strings.HasPrefix(intPart, "-")
	intPart = strings.TrimPrefix(intPart, "-")
	var b strings.Builder
	for i, r := range intPart {
		if i > 0 && (len(intPart)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	out := "$" + b.String() + "." + frac
	if neg {
		out = "-" + out
	}
	return out
}

type opsSpendItem struct {
	name string
	usd  float64
}

// opsSpendSection renders a heading with the section total and one line per
// item (largest first, capped), each with its share of the section.
func opsSpendSection(title string, items []opsSpendItem, extra string) (string, float64) {
	var total float64
	for _, it := range items {
		total += it.usd
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s%s\n", title, fmtUSD(total), extra)
	for i, it := range items {
		if i == opsDailySpendTopLines {
			var rest float64
			for _, r := range items[i:] {
				rest += r.usd
			}
			fmt.Fprintf(&b, "· 其他 %d 项 %s\n", len(items)-i, fmtUSD(rest))
			break
		}
		share := ""
		if total > 0 {
			share = fmt.Sprintf("（%.0f%%）", it.usd*100/total)
		}
		fmt.Fprintf(&b, "· %s %s%s\n", it.name, fmtUSD(it.usd), share)
	}
	return b.String(), total
}

func opsMainSiteSpend(ctx context.Context, start, end time.Time) ([]opsSpendItem, int64, error) {
	// report_daily_agg.hour is a UTC "YYYY-MM-DD HH:00" string.
	from, to := start.UTC().Format("2006-01-02 15:00"), end.UTC().Format("2006-01-02 15:00")
	rows, err := db.QueryContext(ctx, `
		SELECT COALESCE(NULLIF(TRIM(c.tag), ''), NULLIF(split_part(a.channel_name, '-', 2), ''), '(untagged)'),
		       COALESCE(SUM(a.total_cost), 0)::float8, COALESCE(SUM(a.request_count), 0)::bigint
		FROM report_daily_agg a LEFT JOIN channels c ON c.id = a.channel_id
		WHERE a.hour >= $1 AND a.hour < $2
		GROUP BY 1 ORDER BY 2 DESC`, from, to)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var items []opsSpendItem
	var requests int64
	for rows.Next() {
		var it opsSpendItem
		var n int64
		if err := rows.Scan(&it.name, &it.usd, &n); err != nil {
			return nil, 0, err
		}
		requests += n
		if it.usd > 0 {
			items = append(items, it)
		}
	}
	return items, requests, rows.Err()
}

// opsRemoteSpend is each profile's used-quota growth between the last
// snapshot captures at or before the day's two midnights (see
// handleBillingRemoteTagUsage); snapshots run every ~15 min.
func opsRemoteSpend(ctx context.Context, start, end time.Time) ([]opsSpendItem, error) {
	rows, err := db.QueryContext(ctx, `
		WITH b AS (
		  SELECT p.id, p.name,
		         (SELECT MAX(captured_at) FROM remote_channel_snapshot WHERE profile_id = p.id AND captured_at <= $1) AS cap_from,
		         (SELECT MAX(captured_at) FROM remote_channel_snapshot WHERE profile_id = p.id AND captured_at <= $2) AS cap_to
		  FROM remote_newapi_profile p
		)
		SELECT b.name, SUM(GREATEST(t.used_quota - COALESCE(f.used_quota, 0), 0))::bigint
		FROM b
		JOIN remote_channel_snapshot t ON t.profile_id = b.id AND t.captured_at = b.cap_to
		LEFT JOIN remote_channel_snapshot f ON f.profile_id = b.id AND f.captured_at = b.cap_from AND f.remote_channel_id = t.remote_channel_id
		WHERE b.cap_from IS NOT NULL AND b.cap_to > b.cap_from
		GROUP BY 1 ORDER BY 2 DESC`, start.Unix(), end.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []opsSpendItem
	for rows.Next() {
		var name string
		var quota int64
		if err := rows.Scan(&name, &quota); err != nil {
			return nil, err
		}
		if quota > 0 {
			items = append(items, opsSpendItem{name: name, usd: float64(quota) / quotaPerUnit})
		}
	}
	return items, rows.Err()
}

// opsThirdPartySpend returns one item per third-party system plus a note line
// for each system that could not be read.
func opsThirdPartySpend(ctx context.Context, day string, start, end time.Time) ([]opsSpendItem, []string) {
	fx := supplierFxRate()
	var items []opsSpendItem
	var notes []string

	if token := effectiveOpenAPIToken(); token != "" {
		rows, err := db.QueryContext(ctx, `SELECT remote_account_id, provider FROM rs_supplier_account WHERE remote_account_id > 0`)
		if err != nil {
			notes = append(notes, "账号上号 读取失败："+err.Error())
		} else {
			provider := map[int64]string{}
			var ids []int64
			for rows.Next() {
				var id int64
				var p string
				if rows.Scan(&id, &p) == nil {
					provider[id] = p
					ids = append(ids, id)
				}
			}
			rows.Close()
			if len(ids) > 0 {
				costs, err := supplierBillCostRMB(token, ids, day, day)
				if err != nil {
					notes = append(notes, "账号上号 获取失败："+err.Error())
				} else {
					byProvider := map[string]float64{}
					for id, rmb := range costs {
						byProvider[provider[id]] += rmb
					}
					for p, rmb := range byProvider {
						if rmb > 0 {
							items = append(items, opsSpendItem{name: "账号上号 " + p, usd: rmb / fx})
						}
					}
				}
			}
		}
	}

	if through := supplierConfigGet(cfgCCMaxBillSyncedThrough); through >= day {
		var micro int64
		if err := db.QueryRowContext(ctx, `SELECT COALESCE(SUM(cost_micro), 0)::bigint FROM rs_ccmax_daily_bill WHERE ds = $1`, day).Scan(&micro); err != nil {
			notes = append(notes, "CC Max 读取失败："+err.Error())
		} else if micro > 0 {
			items = append(items, opsSpendItem{name: "CC Max（原价）", usd: float64(micro) / 1e6 / fx})
		}
	} else if through != "" {
		notes = append(notes, "CC Max 账单还没同步到 "+day)
	}

	if keyhubConfigured() {
		q := url.Values{}
		q.Set("start_timestamp", strconv.FormatInt(start.Unix(), 10))
		q.Set("end_timestamp", strconv.FormatInt(end.Unix()-1, 10))
		st, body, err := keyhubProxy(http.MethodGet, "/api/provider/keyhub-usage/dashboard/overview?"+q.Encode(), nil)
		var env struct {
			Data struct {
				Summary struct {
					RawCostUSD float64 `json:"raw_cost_usd"`
				} `json:"summary"`
			} `json:"data"`
		}
		switch {
		case err != nil:
			notes = append(notes, "KHub 获取失败："+err.Error())
		case st < 200 || st >= 300:
			notes = append(notes, "KHub 获取失败："+keyhubErr(body))
		case json.Unmarshal(body, &env) != nil:
			notes = append(notes, "KHub 返回无法解析")
		case env.Data.Summary.RawCostUSD > 0:
			items = append(items, opsSpendItem{name: "KHub", usd: env.Data.Summary.RawCostUSD})
		}
	}

	slices.SortFunc(items, func(a, b opsSpendItem) int { return cmp.Compare(b.usd, a.usd) })
	return items, notes
}

// buildOpsDailySpend returns the title and body of day's (UTC+8, YYYY-MM-DD)
// spend report.
func buildOpsDailySpend(ctx context.Context, day string) (string, string, error) {
	start, err := time.ParseInLocation("2006-01-02", day, supplierPortalLoc)
	if err != nil {
		return "", "", err
	}
	end := start.AddDate(0, 0, 1)
	var sections []string
	var grand float64

	mainSite, requests, err := opsMainSiteSpend(ctx, start, end)
	if err != nil {
		return "", "", fmt.Errorf("主站: %w", err)
	}
	sec, total := opsSpendSection("主站", mainSite, fmt.Sprintf(" · %d 次请求", requests))
	sections, grand = append(sections, sec), grand+total

	remote, err := opsRemoteSpend(ctx, start, end)
	if err != nil {
		return "", "", fmt.Errorf("remote: %w", err)
	}
	sec, total = opsSpendSection("Remote", remote, "")
	sections, grand = append(sections, sec), grand+total

	third, notes := opsThirdPartySpend(ctx, day, start, end)
	sec, total = opsSpendSection("第三方", third, "")
	for _, n := range notes {
		sec += "  " + n + "\n"
	}
	sections, grand = append(sections, sec), grand+total

	title := fmt.Sprintf("【每日消耗】%s 合计 %s", start.Format("01-02"), fmtUSD(grand))
	body := strings.Join(sections, "") + fmt.Sprintf("时段 %s 00:00–24:00（UTC+8），第三方 RMB 按 %.2f 换算", start.Format("01-02"), supplierFxRate())
	return title, body, nil
}

// runOpsDailySpend sends yesterday's report once DailySpendHour (UTC+8) has
// passed. The day is claimed in report_config first so it goes out once
// across nodes and restarts.
func runOpsDailySpend(ctx context.Context, cfg opsNotifyConfig) error {
	now := time.Now().In(supplierPortalLoc)
	if now.Hour() < cfg.DailySpendHour {
		return nil
	}
	day := now.AddDate(0, 0, -1).Format("2006-01-02")
	last := supplierConfigGet(cfgOpsDailySpendDay)
	if last >= day {
		return nil
	}
	res, err := db.ExecContext(ctx, `
		INSERT INTO report_config (key, value, updated_at) VALUES ($1, $2, $3)
		ON CONFLICT (key) DO UPDATE SET value = $2, updated_at = $3 WHERE report_config.value < $2`,
		cfgOpsDailySpendDay, day, now.Unix())
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return nil
	}
	title, body, err := buildOpsDailySpend(ctx, day)
	if err != nil {
		return err
	}
	deliverOpsMessage(cfg, "daily_spend", title, body)
	return nil
}

// handleOpsDailySpendPush sends a day's report now (default yesterday, UTC+8)
// without touching the daily claim.
func handleOpsDailySpendPush(c *gin.Context) {
	day := c.Query("date")
	if day == "" {
		day = time.Now().In(supplierPortalLoc).AddDate(0, 0, -1).Format("2006-01-02")
	}
	title, body, err := buildOpsDailySpend(c.Request.Context(), day)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	deliverOpsMessage(loadOpsNotifyConfig(), "daily_spend", title, body)
	log.Printf("[ops-notify] daily spend %s pushed manually", day)
	c.JSON(http.StatusOK, gin.H{"ok": true, "title": title})
}
