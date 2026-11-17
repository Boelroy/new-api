package main

// 运维通知: one Lark webhook plus an in-app list for what operators want to
// hear about without watching every page —
//
//   - 上号: who uploaded keys and, for local OpenAI keys, at which tier;
//   - 优先级: who changed channel priorities by hand (recorded in
//     rs_notify_event); the automatic schedulers are left out;
//   - 断流上升 / 429 上升: a model's stream-failure or 429 rate in the last
//     window against the window before it;
//   - 用量变化: overall / per-model RPM dropping or jumping against the
//     window before it.
//
// Every window (default 10 min) the leader compresses the uploads and
// priority changes of that window into one digest message, and checks the
// rates. A rise is reported once per model per dedupe period. Each message is
// stored in rs_notify_message (the 通知 page) and, with a webhook set, pushed
// to Lark.

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"maps"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/lib/pq"
)

type opsNotifyConfig struct {
	Enabled bool `json:"enabled"`
	// Lark webhook; empty keeps messages in-app only.
	Webhook   string `json:"webhook"`
	WindowMin int    `json:"window_min"`

	Uploads       bool `json:"uploads"`
	Priority      bool `json:"priority"`
	StreamRise    bool `json:"stream_rise"`
	RateLimitRise bool `json:"rate_limit_rise"`
	// RPM change: overall and per-model successful requests per minute in the
	// window against the baseline. A drop to (100-RPMDropPct)% or less, or a
	// rise to RPMRisePct% or more, alerts when the busier side reaches
	// RPMMinBase requests/min.
	RPMChange  bool `json:"rpm_change"`
	RPMDropPct int  `json:"rpm_drop_pct"`
	RPMRisePct int  `json:"rpm_rise_pct"`
	RPMMinBase int  `json:"rpm_min_base"`

	// A rate has risen when the last window's rate is at least RisePct% of
	// the rate over the BaselineMin minutes before it.
	BaselineMin int `json:"baseline_min"`
	RisePct     int `json:"rise_pct"`
	// Floors so a handful of requests cannot raise an alert.
	StreamMinStreams int `json:"stream_min_streams"`
	StreamMinFailed  int `json:"stream_min_failed"`
	StreamMinRatePct int `json:"stream_min_rate_pct"`
	RLMinCount       int `json:"rl_min_count"`
	RLMinRatePct     int `json:"rl_min_rate_pct"`
	DedupeMin        int `json:"dedupe_min"`
}

const (
	cfgOpsNotify         = "ops_notify_config"
	cfgOpsNotifyDigestAt = "ops_notify_digest_at"
	opsNotifyRetention   = 30 * 24 * time.Hour
	// Events younger than this are left for the next digest, so a key created
	// seconds before the tick has had its tier probed.
	opsNotifyDigestLag = 60
	opsDigestMaxLines  = 20
)

var opsNotifyDefaults = opsNotifyConfig{
	Enabled: true, WindowMin: 10,
	Uploads: true, Priority: true, StreamRise: true, RateLimitRise: true,
	RPMChange: true, RPMDropPct: 50, RPMRisePct: 200, RPMMinBase: 30,
	BaselineMin: 10, RisePct: 200,
	StreamMinStreams: 50, StreamMinFailed: 10, StreamMinRatePct: 3,
	RLMinCount: 30, RLMinRatePct: 5, DedupeMin: 60,
}

func (c opsNotifyConfig) clamp() opsNotifyConfig {
	c.Webhook = strings.TrimSpace(c.Webhook)
	// Window + baseline is how far back each check scans logs; keep it short.
	c.WindowMin = clampInt(c.WindowMin, 1, 60)
	c.BaselineMin = clampInt(c.BaselineMin, 1, 60)
	c.RisePct = clampInt(c.RisePct, 101, 10000)
	c.StreamMinStreams = clampInt(c.StreamMinStreams, 1, 1000000)
	c.StreamMinFailed = clampInt(c.StreamMinFailed, 1, 1000000)
	c.StreamMinRatePct = clampInt(c.StreamMinRatePct, 0, 100)
	c.RLMinCount = clampInt(c.RLMinCount, 1, 1000000)
	c.RLMinRatePct = clampInt(c.RLMinRatePct, 0, 100)
	c.DedupeMin = clampInt(c.DedupeMin, 1, 7*24*60)
	c.RPMDropPct = clampInt(c.RPMDropPct, 1, 100)
	c.RPMRisePct = clampInt(c.RPMRisePct, 101, 100000)
	c.RPMMinBase = clampInt(c.RPMMinBase, 1, 1000000)
	return c
}

func loadOpsNotifyConfig() opsNotifyConfig {
	cfg := opsNotifyDefaults
	if raw := supplierConfigGet(cfgOpsNotify); raw != "" {
		if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
			log.Printf("[ops-notify] bad %s, using defaults: %v", cfgOpsNotify, err)
			cfg = opsNotifyDefaults
		}
	}
	return cfg.clamp()
}

// ---- Operator actions recorded for the digest ----

const (
	opsEventChannelCreate = "channel_create"
	opsEventPrioritySet   = "priority_set"
)

type opsChannelCreate struct {
	Studio string  `json:"studio"`
	Type   int     `json:"type"`
	IDs    []int64 `json:"ids"`
}

// opsPrioritySet is one priority edit: Count channels set to To, From counts
// the channels per previous priority. ProfileID 0 = local channels.
type opsPrioritySet struct {
	ProfileID int64            `json:"profile_id,omitempty"`
	To        int64            `json:"to"`
	Count     int              `json:"count"`
	From      map[string]int64 `json:"from"`
}

// recordOpsEvent stores an operator action for the next digest. The action
// itself already succeeded, so a failure here is only logged.
func recordOpsEvent(c *gin.Context, kind string, detail any) {
	var uid int64
	if v, ok := c.Get("user_id"); ok {
		uid, _ = v.(int64)
	}
	name := c.GetString("username")
	raw, _ := json.Marshal(detail)
	if _, err := db.Exec(`INSERT INTO rs_notify_event (kind, actor_id, actor, detail, created_at) VALUES ($1,$2,$3,$4,$5)`,
		kind, uid, name, string(raw), time.Now().Unix()); err != nil {
		log.Printf("[ops-notify] record %s: %v", kind, err)
	}
}

// ---- Digest: uploads + priority changes of one window ----

// opsLineAgg compresses many events of one actor doing one kind of thing into
// a single line, e.g. "alice 本地 OpenAI @studio 12 个：T5×3 T4×9".
type opsLineAgg struct {
	order []string
	lines map[string]*opsLine
}

type opsLine struct {
	head  string
	count int64
	parts map[string]int64
}

func (a *opsLineAgg) add(head string, count int64, part string, partCount int64) {
	if a.lines == nil {
		a.lines = map[string]*opsLine{}
	}
	l := a.lines[head]
	if l == nil {
		l = &opsLine{head: head, parts: map[string]int64{}}
		a.lines[head] = l
		a.order = append(a.order, head)
	}
	l.count += count
	if part != "" {
		l.parts[part] += partCount
	}
}

// render prints one line per head; parts are listed largest first.
func (a *opsLineAgg) render(unit string) []string {
	out := make([]string, 0, len(a.order))
	for _, head := range a.order {
		l := a.lines[head]
		var b strings.Builder
		fmt.Fprintf(&b, "· %s %d %s", head, l.count, unit)
		if len(l.parts) > 0 {
			keys := slices.Collect(maps.Keys(l.parts))
			slices.SortFunc(keys, func(x, y string) int {
				if l.parts[x] != l.parts[y] {
					return int(l.parts[y] - l.parts[x])
				}
				return strings.Compare(x, y)
			})
			parts := make([]string, len(keys))
			for i, k := range keys {
				parts[i] = fmt.Sprintf("%s×%d", k, l.parts[k])
			}
			b.WriteString("：" + strings.Join(parts, " "))
		}
		out = append(out, b.String())
	}
	return out
}

func opsActor(name string, id int64) string {
	if name != "" {
		return name
	}
	if id > 0 {
		return fmt.Sprintf("用户#%d", id)
	}
	return "未知用户"
}

func opsUploadLines(ctx context.Context, from, to int64) ([]string, error) {
	var agg opsLineAgg

	rows, err := db.QueryContext(ctx, `SELECT actor_id, actor, detail FROM rs_notify_event
		WHERE kind = $1 AND created_at > $2 AND created_at <= $3 ORDER BY id`, opsEventChannelCreate, from, to)
	if err != nil {
		return nil, err
	}
	type created struct {
		actor string
		ev    opsChannelCreate
	}
	var creates []created
	var openaiIDs []int64
	for rows.Next() {
		var id int64
		var name, raw string
		if err := rows.Scan(&id, &name, &raw); err != nil {
			rows.Close()
			return nil, err
		}
		var ev opsChannelCreate
		if json.Unmarshal([]byte(raw), &ev) != nil {
			continue
		}
		creates = append(creates, created{actor: opsActor(name, id), ev: ev})
		if ev.Type == 1 {
			openaiIDs = append(openaiIDs, ev.IDs...)
		}
	}
	rows.Close()
	tiers := map[int64]int64{}
	if len(openaiIDs) > 0 {
		rows, err := db.QueryContext(ctx, `SELECT channel_id, tier FROM rs_key_tier WHERE channel_id = ANY($1) AND tier IS NOT NULL`, pq.Array(openaiIDs))
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id, tier int64
			if err := rows.Scan(&id, &tier); err != nil {
				rows.Close()
				return nil, err
			}
			tiers[id] = tier
		}
		rows.Close()
	}
	for _, cr := range creates {
		head := cr.actor + " 本地 " + channelTypeLabel(cr.ev.Type)
		if cr.ev.Studio != "" {
			head += " @" + cr.ev.Studio
		}
		if cr.ev.Type != 1 {
			agg.add(head, int64(len(cr.ev.IDs)), "", 0)
			continue
		}
		for _, id := range cr.ev.IDs {
			part := "未测"
			if t, ok := tiers[id]; ok {
				part = "T" + strconv.FormatInt(t, 10)
			}
			agg.add(head, 1, part, 1)
		}
	}

	// Uploads the other flows already attribute to their uploader.
	sources := []struct{ query, label string }{
		{`SELECT p.uploaded_by, COALESCE(u.username,''), '远程 ' || COALESCE(pr.name, '#' || p.profile_id::text), p.channel_type, COUNT(*)
		  FROM remote_pending_key p
		  LEFT JOIN rs_auth_user u ON u.id = p.uploaded_by
		  LEFT JOIN remote_newapi_profile pr ON pr.id = p.profile_id
		  WHERE p.created_at > $1 AND p.created_at <= $2 GROUP BY 1, 2, 3, 4`, ""},
		{`SELECT a.uploaded_by, COALESCE(u.username,''), a.provider, -1, COUNT(*)
		  FROM rs_supplier_account a LEFT JOIN rs_auth_user u ON u.id = a.uploaded_by
		  WHERE a.created_at > $1 AND a.created_at <= $2 GROUP BY 1, 2, 3`, "账号上号"},
		{`SELECT a.uploaded_by, COALESCE(u.username,''), a.provider, -1, COUNT(*)
		  FROM rs_gynh_account a LEFT JOIN rs_auth_user u ON u.id = a.uploaded_by
		  WHERE a.created_at > $1 AND a.created_at <= $2 GROUP BY 1, 2, 3`, "供给台"},
		{`SELECT a.uploaded_by, COALESCE(u.username,''), COALESCE(NULLIF(a.plan,''), 'claude'), -1, COUNT(*)
		  FROM rs_ccmax_account a LEFT JOIN rs_auth_user u ON u.id = a.uploaded_by
		  WHERE a.created_at > $1 AND a.created_at <= $2 GROUP BY 1, 2, 3`, "CC Max"},
	}
	for _, src := range sources {
		rows, err := db.QueryContext(ctx, src.query, from, to)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id, n int64
			var name, what string
			var chType int
			if err := rows.Scan(&id, &name, &what, &chType, &n); err != nil {
				rows.Close()
				return nil, err
			}
			head := opsActor(name, id) + " "
			if src.label != "" {
				head += src.label + " "
			}
			head += what
			if chType >= 0 {
				head += " " + channelTypeLabel(chType)
			}
			agg.add(head, n, "", 0)
		}
		rows.Close()
	}
	return agg.render("个"), nil
}

func opsPriorityLines(ctx context.Context, from, to int64) ([]string, error) {
	profiles := map[int64]string{}
	if rows, err := db.QueryContext(ctx, `SELECT id, name FROM remote_newapi_profile`); err == nil {
		for rows.Next() {
			var id int64
			var name string
			if rows.Scan(&id, &name) == nil {
				profiles[id] = name
			}
		}
		rows.Close()
	}

	var manual opsLineAgg
	rows, err := db.QueryContext(ctx, `SELECT actor_id, actor, detail FROM rs_notify_event
		WHERE kind = $1 AND created_at > $2 AND created_at <= $3 ORDER BY id`, opsEventPrioritySet, from, to)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id int64
		var name, raw string
		if err := rows.Scan(&id, &name, &raw); err != nil {
			rows.Close()
			return nil, err
		}
		var ev opsPrioritySet
		if json.Unmarshal([]byte(raw), &ev) != nil {
			continue
		}
		scope := "本地"
		if ev.ProfileID > 0 {
			scope = "远程 " + cmp.Or(profiles[ev.ProfileID], "#"+strconv.FormatInt(ev.ProfileID, 10))
		}
		head := fmt.Sprintf("%s %s → %d", opsActor(name, id), scope, ev.To)
		if len(ev.From) == 0 {
			manual.add(head, int64(ev.Count), "", 0)
			continue
		}
		for prio, n := range ev.From {
			manual.add(head, n, "原 "+prio, n)
		}
	}
	rows.Close()
	return manual.render("个渠道"), nil
}

func opsClockRange(from, to int64) string {
	f, t := time.Unix(from, 0).In(supplierPortalLoc), time.Unix(to, 0).In(supplierPortalLoc)
	return f.Format("01-02 15:04") + "–" + t.Format("15:04")
}

func buildOpsDigest(ctx context.Context, cfg opsNotifyConfig, from, to int64) (string, error) {
	type section struct {
		title string
		lines []string
	}
	var sections []section
	if cfg.Uploads {
		lines, err := opsUploadLines(ctx, from, to)
		if err != nil {
			return "", fmt.Errorf("uploads: %w", err)
		}
		sections = append(sections, section{"上号", lines})
	}
	if cfg.Priority {
		lines, err := opsPriorityLines(ctx, from, to)
		if err != nil {
			return "", fmt.Errorf("priority: %w", err)
		}
		sections = append(sections, section{"优先级", lines})
	}
	var b strings.Builder
	for _, s := range sections {
		if len(s.lines) == 0 {
			continue
		}
		b.WriteString(s.title + "\n")
		for i, l := range s.lines {
			if i == opsDigestMaxLines {
				fmt.Fprintf(&b, "· …另 %d 条\n", len(s.lines)-i)
				break
			}
			b.WriteString(l + "\n")
		}
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

// ---- Rising stream-failure / 429 rates ----

// riseRow is one model's traffic in the last window and in the baseline
// window before it. Hits are failed streams or 429s; Total is what they are a
// share of.
type riseRow struct {
	Model     string
	CurTotal  int64
	CurHits   int64
	BaseTotal int64
	BaseHits  int64
}

type riseRule struct {
	MinTotal   int64
	MinHits    int64
	MinRatePct int64
	RisePct    int64
}

// riseVerdict returns the rows whose current rate clears the floors and is at
// least RisePct% of the baseline rate (a model with no baseline hits always
// qualifies), most hits first.
func riseVerdict(rule riseRule, rows []riseRow) []riseRow {
	var out []riseRow
	for _, r := range rows {
		if r.CurTotal <= 0 || r.CurTotal < rule.MinTotal || r.CurHits < rule.MinHits ||
			r.CurHits*100 < rule.MinRatePct*r.CurTotal {
			continue
		}
		// cur/curTotal >= RisePct/100 * base/baseTotal, cross-multiplied.
		if r.BaseHits > 0 && r.BaseTotal > 0 &&
			r.CurHits*r.BaseTotal*100 < rule.RisePct*r.BaseHits*r.CurTotal {
			continue
		}
		out = append(out, r)
	}
	slices.SortFunc(out, func(a, b riseRow) int {
		if a.CurHits != b.CurHits {
			return int(b.CurHits - a.CurHits)
		}
		return strings.Compare(a.Model, b.Model)
	})
	return out
}

func pct(hits, total int64) string {
	if total <= 0 {
		return "0%"
	}
	return strconv.FormatFloat(float64(hits)*100/float64(total), 'f', 1, 64) + "%"
}

var (
	opsAlertMu   sync.Mutex
	opsAlertLast = map[string]time.Time{}
)

// opsAlertDue reports whether key may alert again and, if so, marks it.
func opsAlertDue(key string, every time.Duration) bool {
	opsAlertMu.Lock()
	defer opsAlertMu.Unlock()
	if t, ok := opsAlertLast[key]; ok && time.Since(t) < every {
		return false
	}
	opsAlertLast[key] = time.Now()
	return true
}

type opsRiseCheck struct {
	kind  string // alert dedupe key prefix + message category
	title string
	what  string
	rule  riseRule
	// rowsSQL: $1 baseline start, $2 current window start; returns model,
	// is_current, total, hits.
	rowsSQL string
	// topSQL: $1 current window start, $2 models; returns model, channel id,
	// channel name, hits, total.
	topSQL string
}

// opsStreamFailed / ops429 match a failed stream / a 429 error row of the
// logs table aliased l.
const (
	opsStreamFailed = `l.other LIKE '%"response_status":"failed"%'`
	ops429          = `(l.content LIKE '%status_code=429%' OR l.content LIKE '%StatusCode: 429%')`
)

func opsRiseChecks(cfg opsNotifyConfig) []opsRiseCheck {
	var checks []opsRiseCheck
	if cfg.StreamRise {
		checks = append(checks, opsRiseCheck{
			kind: "stream_rise", title: "断流上升", what: "断流",
			rule: riseRule{MinTotal: int64(cfg.StreamMinStreams), MinHits: int64(cfg.StreamMinFailed),
				MinRatePct: int64(cfg.StreamMinRatePct), RisePct: int64(cfg.RisePct)},
			rowsSQL: `SELECT l.model_name, l.created_at >= $2, COUNT(*), COUNT(*) FILTER (WHERE ` + opsStreamFailed + `)
				FROM logs l WHERE l.created_at >= $1 AND l.type = 2 AND l.is_stream GROUP BY 1, 2`,
			topSQL: `SELECT l.model_name, l.channel_id, COALESCE(c.name,''),
				COUNT(*) FILTER (WHERE ` + opsStreamFailed + `), COUNT(*)
				FROM logs l LEFT JOIN channels c ON c.id = l.channel_id
				WHERE l.created_at >= $1 AND l.type = 2 AND l.is_stream AND l.model_name = ANY($2)
				GROUP BY 1, 2, 3 HAVING COUNT(*) FILTER (WHERE ` + opsStreamFailed + `) > 0
				ORDER BY 4 DESC`,
		})
	}
	if cfg.RateLimitRise {
		checks = append(checks, opsRiseCheck{
			kind: "rate_limit_rise", title: "429 上升", what: "429",
			rule: riseRule{MinHits: int64(cfg.RLMinCount), MinRatePct: int64(cfg.RLMinRatePct), RisePct: int64(cfg.RisePct)},
			// Total = successes + 429s, so the rate is the share of attempts throttled.
			rowsSQL: `SELECT l.model_name, l.created_at >= $2,
				COUNT(*) FILTER (WHERE l.type = 2 OR ` + ops429 + `), COUNT(*) FILTER (WHERE l.type = 5 AND ` + ops429 + `)
				FROM logs l WHERE l.created_at >= $1 AND l.type IN (2, 5) GROUP BY 1, 2`,
			topSQL: `SELECT l.model_name, l.channel_id, COALESCE(c.name,''), COUNT(*), 0
				FROM logs l LEFT JOIN channels c ON c.id = l.channel_id
				WHERE l.created_at >= $1 AND l.type = 5 AND l.model_name = ANY($2) AND ` + ops429 + `
				GROUP BY 1, 2, 3 ORDER BY 4 DESC`,
		})
	}
	return checks
}

// runOpsRiseCheck returns the alert body for one check, "" when nothing rose
// (or every riser alerted within the dedupe period).
func runOpsRiseCheck(ctx context.Context, cfg opsNotifyConfig, chk opsRiseCheck, now int64) (string, error) {
	curStart := now - int64(cfg.WindowMin)*60
	baseStart := curStart - int64(cfg.BaselineMin)*60
	rows, err := db.QueryContext(ctx, chk.rowsSQL, baseStart, curStart)
	if err != nil {
		return "", err
	}
	byModel := map[string]*riseRow{}
	var all riseRow
	for rows.Next() {
		var model string
		var cur bool
		var total, hits int64
		if err := rows.Scan(&model, &cur, &total, &hits); err != nil {
			rows.Close()
			return "", err
		}
		r := byModel[model]
		if r == nil {
			r = &riseRow{Model: model}
			byModel[model] = r
		}
		if cur {
			r.CurTotal += total
			r.CurHits += hits
			all.CurTotal += total
			all.CurHits += hits
		} else {
			r.BaseTotal += total
			r.BaseHits += hits
			all.BaseTotal += total
			all.BaseHits += hits
		}
	}
	rows.Close()
	list := make([]riseRow, 0, len(byModel))
	for _, r := range byModel {
		list = append(list, *r)
	}
	var hits []riseRow
	for _, r := range riseVerdict(chk.rule, list) {
		if opsAlertDue(chk.kind+"|"+r.Model, time.Duration(cfg.DedupeMin)*time.Minute) {
			hits = append(hits, r)
		}
	}
	if len(hits) == 0 {
		return "", nil
	}

	models := make([]string, len(hits))
	for i, h := range hits {
		models[i] = h.Model
	}
	top := map[string][]string{}
	if rows, err := db.QueryContext(ctx, chk.topSQL, curStart, pq.Array(models)); err == nil {
		for rows.Next() {
			var model, name string
			var id, n, total int64
			if rows.Scan(&model, &id, &name, &n, &total) != nil || len(top[model]) >= 3 {
				continue
			}
			s := fmt.Sprintf("#%d %s %d", id, name, n)
			if total > 0 {
				s += "/" + strconv.FormatInt(total, 10)
			}
			top[model] = append(top[model], s)
		}
		rows.Close()
	} else {
		log.Printf("[ops-notify] %s top channels: %v", chk.kind, err)
	}

	var b strings.Builder
	for _, h := range hits {
		fmt.Fprintf(&b, "· %s %s %s（%d/%d），基线 %s\n", h.Model, chk.what, pct(h.CurHits, h.CurTotal), h.CurHits, h.CurTotal, pct(h.BaseHits, h.BaseTotal))
		if t := top[h.Model]; len(t) > 0 {
			b.WriteString("  渠道 " + strings.Join(t, "；") + "\n")
		}
	}
	fmt.Fprintf(&b, "整体 %s（基线 %s）", pct(all.CurHits, all.CurTotal), pct(all.BaseHits, all.BaseTotal))
	return b.String(), nil
}

// ---- RPM / spend change ----

// rpmRow is one model's (or "全部") successful requests and quota in the
// window and the baseline before it.
type rpmRow struct {
	Model     string
	CurReq    int64
	BaseReq   int64
	CurQuota  int64
	BaseQuota int64
}

// rpmVerdict returns the rows whose requests per minute dropped or rose past
// the configured ratios, biggest RPM first. up tells which way each moved.
func rpmVerdict(cfg opsNotifyConfig, rows []rpmRow) (hits []rpmRow, up []bool) {
	for _, r := range rows {
		cur := float64(r.CurReq) / float64(cfg.WindowMin)
		base := float64(r.BaseReq) / float64(cfg.BaselineMin)
		switch {
		case base >= float64(cfg.RPMMinBase) && cur*100 <= base*float64(100-cfg.RPMDropPct):
			hits, up = append(hits, r), append(up, false)
		case cur >= float64(cfg.RPMMinBase) && cur*100 >= base*float64(cfg.RPMRisePct):
			hits, up = append(hits, r), append(up, true)
		}
	}
	return hits, up
}

func changePct(cur, base float64) string {
	if base <= 0 {
		return "新增"
	}
	d := (cur - base) * 100 / base
	if d >= 0 {
		return fmt.Sprintf("↑%.0f%%", d)
	}
	return fmt.Sprintf("↓%.0f%%", -d)
}

// runOpsRPMCheck returns the alert body, "" when no RPM moved (or every mover
// alerted within the dedupe period). Both windows end opsNotifyDigestLag
// before now: a request is logged when it finishes, so the last minute is
// always short and would read as a drop.
func runOpsRPMCheck(ctx context.Context, cfg opsNotifyConfig, now int64) (string, error) {
	end := now - opsNotifyDigestLag
	curStart := end - int64(cfg.WindowMin)*60
	baseStart := curStart - int64(cfg.BaselineMin)*60
	rows, err := db.QueryContext(ctx, `SELECT l.model_name, l.created_at >= $2, COUNT(*), COALESCE(SUM(l.quota), 0)
		FROM logs l WHERE l.created_at >= $1 AND l.created_at < $3 AND l.type = 2 GROUP BY 1, 2`, baseStart, curStart, end)
	if err != nil {
		return "", err
	}
	all := rpmRow{Model: "全部"}
	byModel := map[string]*rpmRow{}
	for rows.Next() {
		var model string
		var cur bool
		var n, quota int64
		if err := rows.Scan(&model, &cur, &n, &quota); err != nil {
			rows.Close()
			return "", err
		}
		r := byModel[model]
		if r == nil {
			r = &rpmRow{Model: model}
			byModel[model] = r
		}
		if cur {
			r.CurReq, r.CurQuota = r.CurReq+n, r.CurQuota+quota
			all.CurReq, all.CurQuota = all.CurReq+n, all.CurQuota+quota
		} else {
			r.BaseReq, r.BaseQuota = r.BaseReq+n, r.BaseQuota+quota
			all.BaseReq, all.BaseQuota = all.BaseReq+n, all.BaseQuota+quota
		}
	}
	rows.Close()
	list := []rpmRow{all}
	for _, r := range byModel {
		list = append(list, *r)
	}
	slices.SortStableFunc(list[1:], func(a, b rpmRow) int {
		return int(max(b.CurReq, b.BaseReq) - max(a.CurReq, a.BaseReq))
	})

	hits, up := rpmVerdict(cfg, list)
	var b strings.Builder
	for i, r := range hits {
		dir := map[bool]string{true: "up", false: "down"}[up[i]]
		if !opsAlertDue("rpm|"+dir+"|"+r.Model, time.Duration(cfg.DedupeMin)*time.Minute) {
			continue
		}
		cur := float64(r.CurReq) / float64(cfg.WindowMin)
		base := float64(r.BaseReq) / float64(cfg.BaselineMin)
		curQ := float64(r.CurQuota) / float64(cfg.WindowMin)
		baseQ := float64(r.BaseQuota) / float64(cfg.BaselineMin)
		fmt.Fprintf(&b, "· %s RPM %.0f → %.0f %s，消耗 %s\n", r.Model, base, cur, changePct(cur, base), changePct(curQ, baseQ))
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

// ---- Delivery + loop ----

var opsCardHeader = map[string]struct{ icon, color string }{
	"digest":          {"📋", "blue"},
	"stream_rise":     {"⚠️", "red"},
	"rate_limit_rise": {"🚦", "orange"},
	"rpm_change":      {"📈", "violet"},
}

var (
	larkMDEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;",
		"*", "&#42;", "_", "&#95;", "~", "&sim;", "`", "&#96;")
	opsChangeRe = regexp.MustCompile(`[↑↓]\d+%`)
)

// opsLarkMD escapes a body line for lark_md, bolds its first word (the
// actor or model) and colours ↓N% red and ↑N% green.
func opsLarkMD(line string) string {
	first, rest, _ := strings.Cut(line, " ")
	out := "**" + larkMDEscaper.Replace(first) + "**"
	if rest != "" {
		out += " " + larkMDEscaper.Replace(rest)
	}
	return opsChangeRe.ReplaceAllStringFunc(out, func(m string) string {
		color := "green"
		if strings.HasPrefix(m, "↓") {
			color = "red"
		}
		return "<font color='" + color + "'>" + m + "</font>"
	})
}

// opsLarkCard renders a stored message as a Lark interactive card: the
// title in a coloured header, every section of the body as its own block
// split by rules ("· " lines become bullets, indented lines grey detail),
// and the 整体 summary plus the send time in the footer.
func opsLarkCard(category, title, body string) map[string]any {
	head := cmp.Or(opsCardHeader[category], opsCardHeader["digest"])
	label, when := title, ""
	if rest, ok := strings.CutPrefix(title, "【"); ok {
		label, when, _ = strings.Cut(rest, "】")
	}
	var elements []any
	var block []string
	footer := []string{}
	for line := range strings.SplitSeq(body, "\n") {
		switch {
		case strings.HasPrefix(line, "· "):
			block = append(block, "• "+opsLarkMD(strings.TrimPrefix(line, "· ")))
		case strings.HasPrefix(line, "  "):
			block = append(block, "　　<font color='grey'>"+larkMDEscaper.Replace(strings.TrimSpace(line))+"</font>")
		case strings.HasPrefix(line, "整体 "):
			footer = append(footer, line)
		case strings.TrimSpace(line) == "":
		default:
			if len(block) > 0 {
				elements = append(elements, map[string]any{"tag": "div", "text": map[string]any{"tag": "lark_md", "content": strings.Join(block, "\n")}},
					map[string]any{"tag": "hr"})
				block = nil
			}
			block = append(block, "**"+larkMDEscaper.Replace(line)+"**")
		}
	}
	if len(block) > 0 {
		elements = append(elements, map[string]any{"tag": "div", "text": map[string]any{"tag": "lark_md", "content": strings.Join(block, "\n")}})
	}
	footer = append(footer, "report-service 运维通知 · "+time.Now().In(supplierPortalLoc).Format("01-02 15:04"))
	elements = append(elements, map[string]any{"tag": "note", "elements": []any{
		map[string]any{"tag": "plain_text", "content": strings.Join(footer, "  |  ")},
	}})
	headerText := head.icon + " " + label
	if when != "" {
		headerText += " · " + when
	}
	return map[string]any{
		"msg_type": "interactive",
		"card": map[string]any{
			"config":   map[string]any{"wide_screen_mode": true},
			"header":   map[string]any{"template": head.color, "title": map[string]any{"tag": "plain_text", "content": headerText}},
			"elements": elements,
		},
	}
}

// deliverOpsMessage stores the message for the 通知 page and pushes it to
// Lark when a webhook is set.
func deliverOpsMessage(cfg opsNotifyConfig, category, title, body string) {
	pushErr := ""
	pushed := false
	if cfg.Webhook != "" {
		if err := postLarkPayload(cfg.Webhook, opsLarkCard(category, title, body)); err != nil {
			pushErr = err.Error()
			log.Printf("[ops-notify] push %s: %v", category, err)
		} else {
			pushed = true
		}
	}
	if _, err := db.Exec(`INSERT INTO rs_notify_message (category, title, body, pushed, push_error, created_at)
		VALUES ($1,$2,$3,$4,$5,$6)`, category, title, body, pushed, pushErr, time.Now().Unix()); err != nil {
		log.Printf("[ops-notify] store %s: %v", category, err)
	}
}

// claimOpsDigestWindow moves the digest high-water mark from its stored value
// to `to` with a compare-and-swap, so two nodes (or a manual run racing the
// loop) never send the same window twice. Returns the window start.
func claimOpsDigestWindow(cfg opsNotifyConfig, to int64, force bool) (int64, bool, error) {
	raw := supplierConfigGet(cfgOpsNotifyDigestAt)
	from, _ := strconv.ParseInt(raw, 10, 64)
	if from == 0 {
		from = to - int64(cfg.WindowMin)*60
	}
	if to <= from || (!force && to-from < int64(cfg.WindowMin)*60) {
		return 0, false, nil
	}
	now := time.Now().Unix()
	var res sql.Result
	var err error
	if raw == "" {
		res, err = db.Exec(`INSERT INTO report_config (key, value, updated_at) VALUES ($1,$2,$3) ON CONFLICT (key) DO NOTHING`,
			cfgOpsNotifyDigestAt, strconv.FormatInt(to, 10), now)
	} else {
		res, err = db.Exec(`UPDATE report_config SET value = $2, updated_at = $3 WHERE key = $1 AND value = $4`,
			cfgOpsNotifyDigestAt, strconv.FormatInt(to, 10), now, raw)
	}
	if err != nil {
		return 0, false, err
	}
	n, _ := res.RowsAffected()
	return from, n == 1, nil
}

// runOpsNotifyTick sends the digest of the window since the last one (when a
// full window has passed, or always when force) and any rate rise. Returns
// how many messages it produced.
func runOpsNotifyTick(ctx context.Context, cfg opsNotifyConfig, force bool) (int, error) {
	now := time.Now().Unix()
	from, claimed, err := claimOpsDigestWindow(cfg, now-opsNotifyDigestLag, force)
	if err != nil || !claimed {
		return 0, err
	}
	to := now - opsNotifyDigestLag
	sent := 0
	var errs []error
	if body, err := buildOpsDigest(ctx, cfg, from, to); err != nil {
		errs = append(errs, err)
	} else if body != "" {
		deliverOpsMessage(cfg, "digest", "【运维动态】"+opsClockRange(from, to), body)
		sent++
	}
	for _, chk := range opsRiseChecks(cfg) {
		body, err := runOpsRiseCheck(ctx, cfg, chk, now)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", chk.kind, err))
			continue
		}
		if body != "" {
			title := fmt.Sprintf("【%s】最近 %d 分钟 vs 前 %d 分钟", chk.title, cfg.WindowMin, cfg.BaselineMin)
			deliverOpsMessage(cfg, chk.kind, title, body)
			sent++
		}
	}
	if cfg.RPMChange {
		if body, err := runOpsRPMCheck(ctx, cfg, now); err != nil {
			errs = append(errs, fmt.Errorf("rpm: %w", err))
		} else if body != "" {
			title := fmt.Sprintf("【用量变化】最近 %d 分钟 vs 前 %d 分钟", cfg.WindowMin, cfg.BaselineMin)
			deliverOpsMessage(cfg, "rpm_change", title, body)
			sent++
		}
	}
	cutoff := time.Now().Add(-opsNotifyRetention).Unix()
	_, err1 := db.ExecContext(ctx, `DELETE FROM rs_notify_event WHERE created_at < $1`, cutoff)
	_, err2 := db.ExecContext(ctx, `DELETE FROM rs_notify_message WHERE created_at < $1`, cutoff)
	errs = append(errs, err1, err2)
	return sent, errors.Join(errs...)
}

func startOpsNotifyLoop() {
	log.Printf("[ops-notify] loop starting")
	go func() {
		time.Sleep(90 * time.Second) // stagger past the other schedulers
		for {
			if cfg := loadOpsNotifyConfig(); cfg.Enabled && IsLeader() {
				if _, err := runOpsNotifyTick(context.Background(), cfg, false); err != nil {
					log.Printf("[ops-notify] tick: %v", err)
				}
			}
			time.Sleep(30 * time.Second)
		}
	}()
}

// ---- HTTP handlers (admin) ----

func handleOpsNotifyConfigGet(c *gin.Context) {
	c.JSON(http.StatusOK, loadOpsNotifyConfig())
}

// handleOpsNotifyConfigSet merges the posted fields over the stored config.
func handleOpsNotifyConfigSet(c *gin.Context) {
	cfg := loadOpsNotifyConfig()
	if err := c.ShouldBindJSON(&cfg); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	cfg = cfg.clamp()
	raw, _ := json.Marshal(cfg)
	if err := supplierConfigSet(cfgOpsNotify, string(raw)); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, cfg)
}

func handleOpsNotifyMessages(c *gin.Context) {
	limit, err := strconv.Atoi(c.Query("limit"))
	if err != nil {
		limit = 200
	}
	limit = clampInt(limit, 1, 1000)
	q := `SELECT id, category, title, body, pushed, push_error, created_at FROM rs_notify_message`
	args := []any{}
	if cat := c.Query("category"); cat != "" {
		q += ` WHERE category = $1`
		args = append(args, cat)
	}
	q += ` ORDER BY id DESC LIMIT ` + strconv.Itoa(limit)
	rows, err := db.Query(q, args...)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()
	items := make([]gin.H, 0)
	for rows.Next() {
		var id, at int64
		var category, title, body, pushErr string
		var pushed bool
		if err := rows.Scan(&id, &category, &title, &body, &pushed, &pushErr, &at); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		items = append(items, gin.H{"id": id, "category": category, "title": title, "body": body,
			"pushed": pushed, "push_error": pushErr, "created_at": at})
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

// handleOpsNotifyRun sends the pending digest and checks the rates now,
// instead of waiting for the window to close.
func handleOpsNotifyRun(c *gin.Context) {
	sent, err := runOpsNotifyTick(c.Request.Context(), loadOpsNotifyConfig(), true)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error(), "sent": sent})
		return
	}
	c.JSON(http.StatusOK, gin.H{"sent": sent})
}

func handleOpsNotifyTest(c *gin.Context) {
	cfg := loadOpsNotifyConfig()
	if cfg.Webhook == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "webhook 未配置"})
		return
	}
	if err := postLarkPayload(cfg.Webhook, opsLarkCard("digest", "【运维通知】测试消息", "· webhook 可用")); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
