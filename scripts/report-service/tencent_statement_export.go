package main

// 腾讯一站 · 月结快照 + Excel export. 「生成对账单」 freezes a month's live
// statement (tencent_statement.go) and the parameters it was built with into
// rs_tencent_statement / rs_tencent_statement_line as a draft. A draft can be
// regenerated from the current data; a confirmed month is final and no longer
// follows the source data. Exports read the snapshot only, so a file always
// matches what was confirmed.

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/xuri/excelize/v2"
)

const (
	tencentStatementDraft     = "draft"
	tencentStatementConfirmed = "confirmed"

	tencentSummarySheet = "汇总表"
	tencentLinesSheet   = "API账单"
)

var errTencentStatementConfirmed = errors.New("该月对账单已确认，不能重新生成")

// tencentStatementHeader is a snapshot's month-level record.
type tencentStatementHeader struct {
	Month       string               `json:"month"`
	Status      string               `json:"status"`
	Meta        tencentStatementMeta `json:"meta"`
	Warnings    []string             `json:"warnings"`
	Lines       int                  `json:"lines"`
	GeneratedAt int64                `json:"generated_at"`
	GeneratedBy int64                `json:"generated_by"`
	ConfirmedAt int64                `json:"confirmed_at"`
	ConfirmedBy int64                `json:"confirmed_by"`
}

// tencentWriteSnapshot builds the month's statement from live data and
// stores it, with the config it was priced with, as a draft. With
// regenerate it replaces an existing draft; without, an existing snapshot of
// either status is left alone and reported.
func tencentWriteSnapshot(month time.Time, regenerate bool, by int64) error {
	start, end := month, month.AddDate(0, 1, 0)
	meta, lines, _, warnings, err := tencentLiveStatement(start, end)
	if err != nil {
		return err
	}
	metaJSON, _ := json.Marshal(meta)
	warningsJSON, _ := json.Marshal(warnings)
	key := month.Format("2006-01")

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var status string
	err = tx.QueryRow(`SELECT status FROM rs_tencent_statement WHERE month = $1 FOR UPDATE`, key).Scan(&status)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if regenerate {
			return sql.ErrNoRows
		}
	case err != nil:
		return err
	case status == tencentStatementConfirmed:
		return errTencentStatementConfirmed
	case !regenerate:
		return errTencentStatementExists
	}
	now := time.Now().Unix()
	if _, err := tx.Exec(`
		INSERT INTO rs_tencent_statement (month, status, fx_rate, ref_fx_rate, params, warnings, generated_at, generated_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (month) DO UPDATE SET fx_rate=EXCLUDED.fx_rate, ref_fx_rate=EXCLUDED.ref_fx_rate, params=EXCLUDED.params,
		  warnings=EXCLUDED.warnings, generated_at=EXCLUDED.generated_at, generated_by=EXCLUDED.generated_by`,
		key, tencentStatementDraft, meta.Config.CCMaxDivisor, meta.Config.BOCRate, string(metaJSON), string(warningsJSON), now, by); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM rs_tencent_statement_line WHERE month = $1`, key); err != nil {
		return err
	}
	for i, l := range lines {
		if _, err := tx.Exec(`
			INSERT INTO rs_tencent_statement_line (month, seq, line_key, platform, pricing, user_id, supplier, bill_month, vendor, model,
			  account_id, key_col, official_usd, tax_rate, discount, tencent_usd, ref_fx_rate, invoice_rmb, bill_name, source)
			VALUES ($1, $2, $3, $4, $5, 0, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19)`,
			key, i, l.LineKey, l.Platform, l.Pricing, l.Supplier, l.BillMonth, l.Vendor, l.Model,
			l.AccountID, l.Key, l.Official, l.TaxRate, l.Discount, l.Tencent, l.RefFxRate, l.InvoiceRMB, l.BillName, l.Source); err != nil {
			return err
		}
	}
	return tx.Commit()
}

var errTencentStatementExists = errors.New("该月对账单已生成，草稿请使用重新生成")

// tencentLoadSnapshot reads a month's snapshot; vendor filters the lines to
// one column C value ("" = all).
func tencentLoadSnapshot(month, vendor string) (tencentStatementHeader, []tencentLine, error) {
	var h tencentStatementHeader
	var meta, warnings string
	err := db.QueryRow(`
		SELECT month, status, params, warnings, generated_at, generated_by, confirmed_at, confirmed_by
		  FROM rs_tencent_statement WHERE month = $1`, month).
		Scan(&h.Month, &h.Status, &meta, &warnings, &h.GeneratedAt, &h.GeneratedBy, &h.ConfirmedAt, &h.ConfirmedBy)
	if err != nil {
		return h, nil, err
	}
	_ = json.Unmarshal([]byte(meta), &h.Meta)
	_ = json.Unmarshal([]byte(warnings), &h.Warnings)
	rows, err := db.Query(`
		SELECT line_key, platform, pricing, supplier, bill_month, vendor, model, account_id, key_col, official_usd, tax_rate,
		       discount, tencent_usd, ref_fx_rate, invoice_rmb, bill_name, source
		  FROM rs_tencent_statement_line
		 WHERE month = $1 AND ($2 = '' OR vendor = $2)
		 ORDER BY seq`, month, vendor)
	if err != nil {
		return h, nil, err
	}
	defer rows.Close()
	lines := []tencentLine{}
	for rows.Next() {
		var l tencentLine
		if err := rows.Scan(&l.LineKey, &l.Platform, &l.Pricing, &l.Supplier, &l.BillMonth, &l.Vendor, &l.Model, &l.AccountID, &l.Key,
			&l.Official, &l.TaxRate, &l.Discount, &l.Tencent, &l.RefFxRate, &l.InvoiceRMB, &l.BillName, &l.Source); err != nil {
			return h, nil, err
		}
		lines = append(lines, l)
	}
	h.Lines = len(lines)
	return h, lines, rows.Err()
}

// Number formats of the template.
const (
	tencentFmtUSD = `#,##0.00" USD";(#,##0.00)" USD";0.00" USD"`
	tencentFmtRMB = `#,##0.00" RMB";(#,##0.00)" RMB";0.00" RMB"`
)

// tencentPricingShort describes a pricing method next to a discount placed
// in the parameter block's second column (as 汇总表 F12 does).
var tencentPricingShort = map[string]string{
	tencentPricingUSD:   "美元计价，含票",
	tencentPricingCCMax: "人民币÷除数计价，含税",
	tencentPricingRMB:   "人民币计价，不另加税费",
}

// tencentStatementWorkbook lays the statement out as 「长河理想公司10月对账单」:
// 「汇总表」 (one row per vendor, 开票金额合计, then the parameter block and
// notes) and 「API账单」 (A–M). I and K refer to the parameter cells and J, L
// and the summary are formulas, so changing a rate or discount in the file
// re-prices it.
func tencentStatementWorkbook(lines []tencentLine, meta tencentStatementMeta) (*excelize.File, error) {
	month, _, err := tencentMonthRange(meta.Month)
	if err != nil {
		return nil, err
	}
	cfg := meta.Config
	f := excelize.NewFile()
	if err := f.SetSheetName("Sheet1", tencentSummarySheet); err != nil {
		return nil, err
	}
	if _, err := f.NewSheet(tencentLinesSheet); err != nil {
		return nil, err
	}
	styles := map[string]int{}
	style := func(numFmt string, bold bool) (int, error) {
		key := fmt.Sprintf("%s|%v", numFmt, bold)
		if id, ok := styles[key]; ok {
			return id, nil
		}
		s := &excelize.Style{Alignment: &excelize.Alignment{Vertical: "center"}}
		if numFmt != "" {
			s.CustomNumFmt = &numFmt
		}
		if bold {
			s.Font = &excelize.Font{Bold: true}
			s.Alignment.WrapText = true
		}
		id, err := f.NewStyle(s)
		styles[key] = id
		return id, err
	}
	// set writes a value or (with a leading =) a formula with a number format.
	set := func(sheet, cell string, v any, numFmt string) error {
		if formula, ok := v.(string); ok && strings.HasPrefix(formula, "=") {
			if err := f.SetCellFormula(sheet, cell, formula[1:]); err != nil {
				return err
			}
		} else if err := f.SetCellValue(sheet, cell, v); err != nil {
			return err
		}
		if numFmt == "" {
			return nil
		}
		id, err := style(numFmt, false)
		if err != nil {
			return err
		}
		return f.SetCellStyle(sheet, cell, cell, id)
	}
	cell := func(col string, row int) string { return fmt.Sprintf("%s%d", col, row) }
	bold, err := style("", true)
	if err != nil {
		return nil, err
	}

	summary := tencentSummarize(lines, cfg)
	// 汇总表 layout: vendors from row 2, the total, a blank row, then the
	// parameter block: BOC rate, CCMax divisor, then each vendor's discount
	// (the first two beside the D notes, the rest two to a row, in A/B and
	// D/E like the template's zhipu).
	totalRow := len(summary) + 2
	rateRow, divRow := totalRow+2, totalRow+3
	vendors := slices.Clone(cfg.Vendors)
	for _, s := range summary {
		if !slices.ContainsFunc(vendors, func(v tencentVendorConfig) bool { return v.Vendor == s.Vendor }) {
			vendors = append(vendors, cfg.vendor(s.Vendor, tencentPlatformOfPricing(s.Pricing)))
		}
	}
	discountCell := map[string]string{}
	lastParamRow := divRow + 2
	for i, v := range vendors {
		v = cfg.vendor(v.Vendor, tencentPlatformOfPricing(v.Pricing))
		row, labelCol, valueCol := divRow+1+i, "A", "B"
		if i >= 2 {
			k := i - 2
			row = divRow + 3 + k/2
			if k%2 == 1 {
				labelCol, valueCol = "D", "E"
			}
		}
		lastParamRow = max(lastParamRow, row)
		discount := 1.0
		if v.Discount != nil {
			discount = *v.Discount
		}
		if err := set(tencentSummarySheet, cell(labelCol, row), v.Label, ""); err != nil {
			return nil, err
		}
		if err := set(tencentSummarySheet, cell(valueCol, row), discount, "0%"); err != nil {
			return nil, err
		}
		if valueCol == "E" {
			if err := set(tencentSummarySheet, cell("F", row), tencentPricingShort[v.Pricing], ""); err != nil {
				return nil, err
			}
		}
		discountCell[v.Vendor] = fmt.Sprintf("'%s'!$%s$%d", tencentSummarySheet, valueCol, row)
	}
	rateRef := fmt.Sprintf("'%s'!$B$%d", tencentSummarySheet, rateRow)
	divRef := fmt.Sprintf("'%s'!$B$%d", tencentSummarySheet, divRow)

	// API账单
	for i, h := range tencentStatementHeaders {
		c, _ := excelize.CoordinatesToCellName(i+1, 1)
		if err := f.SetCellValue(tencentLinesSheet, c, h); err != nil {
			return nil, err
		}
	}
	if err := f.SetCellStyle(tencentLinesSheet, "A1", "M1", bold); err != nil {
		return nil, err
	}
	for col, numFmt := range map[string]string{"B": "0_ ", "C": "0_ ", "I": "0.00%", "L": `0.00_);[Red]\(0.00\)`} {
		id, err := style(numFmt, true)
		if err != nil {
			return nil, err
		}
		if err := f.SetCellStyle(tencentLinesSheet, col+"1", col+"1", id); err != nil {
			return nil, err
		}
	}
	for i, l := range lines {
		r := i + 2
		gFmt, jFmt := tencentFmtRMB, tencentFmtRMB
		jFormula := fmt.Sprintf("=G%d*I%d", r, r)
		var k any = "=" + rateRef
		switch l.Pricing {
		case tencentPricingUSD:
			gFmt, jFmt = tencentFmtUSD, tencentFmtUSD
		case tencentPricingCCMax:
			jFmt = tencentFmtUSD
			jFormula = fmt.Sprintf("=G%d/%s*I%d", r, divRef, r)
		default:
			k = 1
		}
		for _, c := range []struct {
			col    string
			v      any
			numFmt string
		}{
			{"A", l.Supplier, ""}, {"B", l.BillMonth, ""}, {"C", l.Vendor, "0_ "}, {"D", l.Model, ""},
			{"E", l.AccountID, ""}, {"F", l.Key, ""}, {"G", l.Official, gFmt}, {"H", l.TaxRate, ""},
			{"I", "=" + discountCell[l.Vendor], "0%"}, {"J", jFormula, jFmt}, {"K", k, "0.0000"},
			{"L", fmt.Sprintf("=J%d*K%d", r, r), tencentFmtRMB}, {"M", l.BillName, ""},
		} {
			if c.col == "F" && l.Key == "" {
				continue
			}
			if err := set(tencentLinesSheet, cell(c.col, r), c.v, c.numFmt); err != nil {
				return nil, err
			}
		}
	}

	// 汇总表
	for i, h := range tencentSummaryHeaders {
		c, _ := excelize.CoordinatesToCellName(i+1, 1)
		if err := f.SetCellValue(tencentSummarySheet, c, h); err != nil {
			return nil, err
		}
	}
	if err := f.SetCellStyle(tencentSummarySheet, "A1", "F1", bold); err != nil {
		return nil, err
	}
	for col, numFmt := range map[string]string{"B": "0_ ", "E": `0.00_);[Red]\(0.00\)`, "F": `0.00_);[Red]\(0.00\)`} {
		id, err := style(numFmt, true)
		if err != nil {
			return nil, err
		}
		if err := f.SetCellStyle(tencentSummarySheet, col+"1", col+"1", id); err != nil {
			return nil, err
		}
	}
	last := max(len(lines)+1, 2)
	rng := func(col string) string { return fmt.Sprintf("'%s'!$%s$2:$%s$%d", tencentLinesSheet, col, col, last) }
	for i, s := range summary {
		r := i + 2
		dFmt := tencentFmtRMB
		if s.Pricing == tencentPricingUSD {
			dFmt = tencentFmtUSD
		}
		for _, c := range []struct {
			col    string
			v      any
			numFmt string
		}{
			{"A", s.Supplier, ""}, {"B", s.BillMonth, ""}, {"C", s.Vendor, ""},
			{"D", fmt.Sprintf("=SUMIFS(%s,%s,C%d)", rng("G"), rng("C"), r), dFmt},
			{"E", fmt.Sprintf("=ROUND(SUMIFS(%s,%s,C%d),2)", rng("L"), rng("C"), r), tencentFmtRMB},
			{"F", s.Note, ""},
		} {
			if err := set(tencentSummarySheet, cell(c.col, r), c.v, c.numFmt); err != nil {
				return nil, err
			}
		}
	}
	if err := set(tencentSummarySheet, cell("A", totalRow), "开票金额合计", ""); err != nil {
		return nil, err
	}
	if err := set(tencentSummarySheet, cell("E", totalRow), fmt.Sprintf("=ROUND(SUM(E2:E%d),2)", max(totalRow-1, 2)), tencentFmtRMB); err != nil {
		return nil, err
	}

	// Parameter block and notes.
	end := month.AddDate(0, 1, 0)
	lastDay := end.AddDate(0, 0, -1)
	params := []struct {
		cell   string
		v      any
		numFmt string
	}{
		{cell("A", rateRow), cfg.BOCRateDate + " 现汇卖出价", ""},
		{cell("B", rateRow), cfg.BOCRate, "0.0000"},
		{cell("D", rateRow), "中国银行美元现汇卖出价，人民币/1美元", ""},
		{cell("A", divRow), "CCMax RMB/USD 除数", ""},
		{cell("B", divRow), cfg.CCMaxDivisor, "0.0"},
		{cell("D", divRow), fmt.Sprintf("CCMax原RMB金额 ÷ %g × 折扣 × 本期汇率（含税）", cfg.CCMaxDivisor), ""},
		{cell("D", divRow+1), fmt.Sprintf("计费期间：%s 00:00至%s 00:00（北京时间，结束不含）", month.Format("2006-01-02"), end.Format("2006-01-02")), ""},
		{cell("D", divRow+2), "计算保留原精度，金额显示2位；按来源及模型厂商汇总后四舍五入到分", ""},
	}
	for _, p := range params {
		if err := set(tencentSummarySheet, p.cell, p.v, p.numFmt); err != nil {
			return nil, err
		}
	}
	for _, row := range []int{rateRow, divRow, divRow + 1, divRow + 2} {
		if err := f.MergeCell(tencentSummarySheet, cell("D", row), cell("F", row)); err != nil {
			return nil, err
		}
	}
	m := int(month.Month())
	ccmaxNote := "CCMax：官方账户日账单"
	if first := time.Unix(meta.CCMaxFirstAccount, 0).In(billingExportZone); meta.CCMaxFirstAccount > 0 &&
		!first.Before(month.AddDate(0, 0, 1)) && first.Before(end) {
		ccmaxNote += fmt.Sprintf("，首次账户创建于%d月%d日；%d月1日至%d日无账户", m, first.Day(), m, first.Day()-1)
	}
	notes := []string{
		fmt.Sprintf("汇率来源：%s（%s）", tencentBOCRateURL, cfg.BOCRateDate),
		fmt.Sprintf("账单期：%d年%d月1日—%d月%d日（北京时间）。后台原始账单单独附送。", month.Year(), m, m, lastDay.Day()),
		ccmaxNote,
		"原始账单作为独立附件，本工作簿包含对账汇总与API账单明细",
	}
	for i, n := range notes {
		row := lastParamRow + 1 + i
		if err := set(tencentSummarySheet, cell("A", row), n, ""); err != nil {
			return nil, err
		}
		if err := f.MergeCell(tencentSummarySheet, cell("A", row), cell("F", row)); err != nil {
			return nil, err
		}
	}

	for _, w := range []struct {
		sheet, from, to string
		width           float64
	}{
		{tencentSummarySheet, "A", "A", 26}, {tencentSummarySheet, "B", "B", 10}, {tencentSummarySheet, "C", "C", 16},
		{tencentSummarySheet, "D", "E", 22}, {tencentSummarySheet, "F", "F", 44},
		{tencentLinesSheet, "A", "B", 12}, {tencentLinesSheet, "C", "C", 16}, {tencentLinesSheet, "D", "D", 24},
		{tencentLinesSheet, "E", "E", 30}, {tencentLinesSheet, "F", "F", 18}, {tencentLinesSheet, "G", "G", 20},
		{tencentLinesSheet, "H", "I", 10}, {tencentLinesSheet, "J", "J", 20}, {tencentLinesSheet, "K", "K", 14},
		{tencentLinesSheet, "L", "L", 18}, {tencentLinesSheet, "M", "M", 50},
	} {
		if err := f.SetColWidth(w.sheet, w.from, w.to, w.width); err != nil {
			return nil, err
		}
	}
	// No cached values are written for formulas; have Excel compute on open.
	fullCalc := true
	if err := f.SetCalcProps(&excelize.CalcPropsOptions{FullCalcOnLoad: &fullCalc}); err != nil {
		return nil, err
	}
	f.SetActiveSheet(0)
	return f, nil
}

// tencentPlatformOfPricing is the platform whose default pricing is the given
// one, for vendors the config does not name.
func tencentPlatformOfPricing(pricing string) string {
	for p, pr := range tencentPlatformPricing {
		if pr == pricing {
			return p
		}
	}
	return tencentPlatformThirdParty
}

// ---- Handlers ----

func tencentSnapshotMonth(c *gin.Context) (time.Time, bool) {
	start, _, err := tencentMonthRange(c.Param("month"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return time.Time{}, false
	}
	return start, true
}

func handleTencentStatementList(c *gin.Context) {
	// The month's total is the sum of its vendors' L rounded to the cent.
	rows, err := db.Query(`
		SELECT s.month, s.status, s.generated_at, s.confirmed_at,
		       COALESCE(v.lines, 0), COALESCE(v.total, 0)::float8
		  FROM rs_tencent_statement s
		  LEFT JOIN (
		    SELECT month, SUM(n)::int AS lines, SUM(rounded) AS total
		      FROM (SELECT month, COUNT(*) AS n, ROUND(SUM(invoice_rmb)::numeric, 2) AS rounded
		              FROM rs_tencent_statement_line GROUP BY month, vendor) per_vendor
		     GROUP BY month
		  ) v ON v.month = s.month
		 ORDER BY s.month DESC`)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()
	type item struct {
		Month       string  `json:"month"`
		Status      string  `json:"status"`
		GeneratedAt int64   `json:"generated_at"`
		ConfirmedAt int64   `json:"confirmed_at"`
		Lines       int     `json:"lines"`
		InvoiceRMB  float64 `json:"invoice_rmb"`
	}
	list := []item{}
	for rows.Next() {
		var it item
		if err := rows.Scan(&it.Month, &it.Status, &it.GeneratedAt, &it.ConfirmedAt, &it.Lines, &it.InvoiceRMB); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		list = append(list, it)
	}
	c.JSON(http.StatusOK, gin.H{"list": list})
}

func handleTencentStatementGet(c *gin.Context) {
	month, ok := tencentSnapshotMonth(c)
	if !ok {
		return
	}
	h, lines, err := tencentLoadSnapshot(month.Format("2006-01"), "")
	if errors.Is(err, sql.ErrNoRows) {
		c.JSON(http.StatusNotFound, gin.H{"error": "该月尚未生成对账单"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	summary := tencentSummarize(lines, h.Meta.Config)
	c.JSON(http.StatusOK, gin.H{
		"statement":         h,
		"headers":           tencentStatementHeaders,
		"summary_headers":   tencentSummaryHeaders,
		"lines":             lines,
		"summary":           summary,
		"total_invoice_rmb": tencentInvoiceTotal(summary),
	})
}

// handleTencentStatementWrite serves 生成 (regenerate=false: the month must
// have no snapshot) and 重新生成 (regenerate=true: only a draft).
func handleTencentStatementWrite(regenerate bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		month, ok := tencentSnapshotMonth(c)
		if !ok {
			return
		}
		err := tencentWriteSnapshot(month, regenerate, tencentCallerID(c))
		switch {
		case errors.Is(err, sql.ErrNoRows):
			c.JSON(http.StatusNotFound, gin.H{"error": "该月尚未生成对账单"})
			return
		case errors.Is(err, errTencentStatementConfirmed), errors.Is(err, errTencentStatementExists):
			c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
			return
		case err != nil:
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		handleTencentStatementGet(c)
	}
}

func handleTencentStatementConfirm(c *gin.Context) {
	month, ok := tencentSnapshotMonth(c)
	if !ok {
		return
	}
	res, err := db.Exec(`UPDATE rs_tencent_statement SET status = $2, confirmed_at = $3, confirmed_by = $4 WHERE month = $1 AND status = $5`,
		month.Format("2006-01"), tencentStatementConfirmed, time.Now().Unix(), tencentCallerID(c), tencentStatementDraft)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		c.JSON(http.StatusConflict, gin.H{"error": "只有草稿状态的对账单可以确认"})
		return
	}
	handleTencentStatementGet(c)
}

// handleTencentStatementExport downloads a month's snapshot as xlsx: one
// vendor (?vendor=<column C>) or the whole statement.
func handleTencentStatementExport(c *gin.Context) {
	month, ok := tencentSnapshotMonth(c)
	if !ok {
		return
	}
	vendor := strings.TrimSpace(c.Query("vendor"))
	h, lines, err := tencentLoadSnapshot(month.Format("2006-01"), vendor)
	if errors.Is(err, sql.ErrNoRows) {
		c.JSON(http.StatusNotFound, gin.H{"error": "该月尚未生成对账单"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if vendor != "" && len(lines) == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "该厂商在本月对账单中没有明细"})
		return
	}
	f, err := tencentStatementWorkbook(lines, h.Meta)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer f.Close()
	name := fmt.Sprintf("%s公司%d月对账单.xlsx", h.Meta.Config.Company, int(month.Month()))
	if vendor != "" {
		name = fmt.Sprintf("%s公司%d月对账单_%s.xlsx", h.Meta.Config.Company, int(month.Month()), vendor)
	}
	c.Header("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="statement-%s.xlsx"; filename*=UTF-8''%s`,
		month.Format("2006-01"), url.PathEscape(name)))
	if err := f.Write(c.Writer); err != nil {
		c.Status(http.StatusInternalServerError)
	}
}
