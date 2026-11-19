package main

// 腾讯一站 · 月结快照 + Excel export. 「生成对账单」 freezes a month's live
// statement (tencent_statement.go) and the parameters it was built with into
// rs_tencent_statement / rs_tencent_statement_line as a draft. A draft can be
// regenerated from the current data; a confirmed month is final and no longer
// follows the source data. Exports read the snapshot only, so a file always
// matches what was confirmed.

import (
	"cmp"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
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
	Month       string   `json:"month"`
	Status      string   `json:"status"`
	FxRate      float64  `json:"fx_rate"`
	RefFxRate   float64  `json:"ref_fx_rate"`
	Params      any      `json:"params"`
	Warnings    []string `json:"warnings"`
	Lines       int      `json:"lines"`
	GeneratedAt int64    `json:"generated_at"`
	GeneratedBy int64    `json:"generated_by"`
	ConfirmedAt int64    `json:"confirmed_at"`
	ConfirmedBy int64    `json:"confirmed_by"`
}

// tencentWriteSnapshot builds the month's statement from live data and
// stores it as a draft. With regenerate it replaces an existing draft;
// without, an existing snapshot of either status is left alone and reported.
func tencentWriteSnapshot(month time.Time, regenerate bool, by int64) error {
	start, end := month, month.AddDate(0, 1, 0)
	params, _, lines, warnings, err := tencentLiveStatement(start, end)
	if err != nil {
		return err
	}
	paramsJSON, _ := json.Marshal(params)
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
		key, tencentStatementDraft, params.FxRate, params.RefFxRate, string(paramsJSON), string(warningsJSON), now, by); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM rs_tencent_statement_line WHERE month = $1`, key); err != nil {
		return err
	}
	for i, l := range lines {
		if _, err := tx.Exec(`
			INSERT INTO rs_tencent_statement_line (month, seq, line_key, platform, user_id, supplier, bill_month, vendor, model,
			  account_id, key_col, official_usd, tax_rate, discount, tencent_usd, ref_fx_rate, invoice_rmb, bill_name, source,
			  cost_cny, cost_usd)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21)`,
			key, i, l.LineKey, l.Platform, l.UserID, l.Supplier, l.BillMonth, l.Vendor, l.Model,
			l.AccountID, l.Key, l.OfficialUSD, l.TaxRate, l.Discount, l.TencentUSD, l.RefFxRate, l.InvoiceRMB, l.BillName, l.Source,
			l.CostCNY, l.CostUSD); err != nil {
			return err
		}
	}
	return tx.Commit()
}

var errTencentStatementExists = errors.New("该月对账单已生成，草稿请使用重新生成")

// tencentLoadSnapshot reads a month's snapshot; supplier filters the lines to
// one column A value ("" = all).
func tencentLoadSnapshot(month, supplier string) (tencentStatementHeader, []tencentLine, error) {
	var h tencentStatementHeader
	var params, warnings string
	err := db.QueryRow(`
		SELECT month, status, fx_rate, ref_fx_rate, params, warnings, generated_at, generated_by, confirmed_at, confirmed_by
		  FROM rs_tencent_statement WHERE month = $1`, month).
		Scan(&h.Month, &h.Status, &h.FxRate, &h.RefFxRate, &params, &warnings, &h.GeneratedAt, &h.GeneratedBy, &h.ConfirmedAt, &h.ConfirmedBy)
	if err != nil {
		return h, nil, err
	}
	_ = json.Unmarshal([]byte(params), &h.Params)
	_ = json.Unmarshal([]byte(warnings), &h.Warnings)
	rows, err := db.Query(`
		SELECT line_key, platform, user_id, supplier, bill_month, vendor, model, account_id, key_col, official_usd, tax_rate,
		       discount, tencent_usd, ref_fx_rate, invoice_rmb, bill_name, source, cost_cny, cost_usd
		  FROM rs_tencent_statement_line
		 WHERE month = $1 AND ($2 = '' OR supplier = $2)
		 ORDER BY seq`, month, supplier)
	if err != nil {
		return h, nil, err
	}
	defer rows.Close()
	lines := []tencentLine{}
	for rows.Next() {
		var l tencentLine
		if err := rows.Scan(&l.LineKey, &l.Platform, &l.UserID, &l.Supplier, &l.BillMonth, &l.Vendor, &l.Model, &l.AccountID, &l.Key,
			&l.OfficialUSD, &l.TaxRate, &l.Discount, &l.TencentUSD, &l.RefFxRate, &l.InvoiceRMB, &l.BillName, &l.Source,
			&l.CostCNY, &l.CostUSD); err != nil {
			return h, nil, err
		}
		lines = append(lines, l)
	}
	h.Lines = len(lines)
	return h, lines, rows.Err()
}

// tencentStatementWorkbook lays lines out as the template: 「汇总表」 then
// 「API账单」, same headers and number formats, with J, L and the summary
// totals as the template's formulas so edits to G / I / K carry through.
// The summary SUMIFS match vendor (C) and source prefix (N) as the template
// does; a file with several suppliers also matches the supplier (A).
func tencentStatementWorkbook(lines []tencentLine) (*excelize.File, error) {
	f := excelize.NewFile()
	if err := f.SetSheetName("Sheet1", tencentSummarySheet); err != nil {
		return nil, err
	}
	if _, err := f.NewSheet(tencentLinesSheet); err != nil {
		return nil, err
	}
	usd := `#,##0.00" USD"`
	vendorFmt := `0_ `
	styles := map[string]*excelize.Style{
		"header": {Font: &excelize.Font{Bold: true}, Alignment: &excelize.Alignment{WrapText: true, Vertical: "center"}},
		"usd":    {CustomNumFmt: &usd},
		"vendor": {CustomNumFmt: &vendorFmt},
		"pct":    {NumFmt: 9}, // 0%
		"amount": {NumFmt: 4}, // #,##0.00
	}
	ids := map[string]int{}
	for name, s := range styles {
		id, err := f.NewStyle(s)
		if err != nil {
			return nil, err
		}
		ids[name] = id
	}

	lastRow := max(len(lines)+1, 2)
	for i, h := range tencentStatementHeaders {
		cell, _ := excelize.CoordinatesToCellName(i+1, 1)
		if err := f.SetCellValue(tencentLinesSheet, cell, h); err != nil {
			return nil, err
		}
	}
	suppliers := map[string]bool{}
	for i, l := range lines {
		suppliers[l.Supplier] = true
		r := i + 2
		account := any(l.AccountID)
		if n, err := strconv.ParseInt(l.AccountID, 10, 64); err == nil {
			account = n
		}
		values := []any{l.Supplier, l.BillMonth, l.Vendor, l.Model, account, l.Key, l.OfficialUSD, l.TaxRate, l.Discount, nil, l.RefFxRate, nil, l.BillName, l.Source}
		for c, v := range values {
			if v == nil {
				continue
			}
			cell, _ := excelize.CoordinatesToCellName(c+1, r)
			if err := f.SetCellValue(tencentLinesSheet, cell, v); err != nil {
				return nil, err
			}
		}
		if err := f.SetCellFormula(tencentLinesSheet, fmt.Sprintf("J%d", r), fmt.Sprintf("ROUND(G%d*I%d,2)", r, r)); err != nil {
			return nil, err
		}
		if err := f.SetCellFormula(tencentLinesSheet, fmt.Sprintf("L%d", r), fmt.Sprintf(`IF(J%d="","",J%d*K%d)`, r, r, r)); err != nil {
			return nil, err
		}
	}
	for col, style := range map[string]string{"C": "vendor", "G": "usd", "I": "pct", "J": "usd", "L": "amount"} {
		if err := f.SetCellStyle(tencentLinesSheet, col+"2", fmt.Sprintf("%s%d", col, lastRow), ids[style]); err != nil {
			return nil, err
		}
	}
	if err := f.SetCellStyle(tencentLinesSheet, "A1", "N1", ids["header"]); err != nil {
		return nil, err
	}

	for i, h := range tencentSummaryHeaders {
		cell, _ := excelize.CoordinatesToCellName(i+1, 1)
		if err := f.SetCellValue(tencentSummarySheet, cell, h); err != nil {
			return nil, err
		}
	}
	col := func(c string) string {
		return fmt.Sprintf("'%s'!%s$2:%s$%d", tencentLinesSheet, c, c, lastRow)
	}
	summary := tencentSummarize(lines)
	for i, s := range summary {
		r := i + 2
		for c, v := range []any{s.Supplier, s.BillMonth, s.Vendor} {
			cell, _ := excelize.CoordinatesToCellName(c+1, r)
			if err := f.SetCellValue(tencentSummarySheet, cell, v); err != nil {
				return nil, err
			}
		}
		criteria := fmt.Sprintf(`%s,C%d,%s,"%s*"`, col("C"), r, col("N"), s.Source)
		if len(suppliers) > 1 {
			criteria = fmt.Sprintf("%s,A%d,%s", col("A"), r, criteria)
		}
		if err := f.SetCellFormula(tencentSummarySheet, fmt.Sprintf("D%d", r), fmt.Sprintf("SUMIFS(%s,%s)", col("G"), criteria)); err != nil {
			return nil, err
		}
		if err := f.SetCellFormula(tencentSummarySheet, fmt.Sprintf("E%d", r), fmt.Sprintf("SUMIFS(%s,%s)", col("L"), criteria)); err != nil {
			return nil, err
		}
	}
	summaryLast := max(len(summary)+1, 2)
	if err := f.SetCellStyle(tencentSummarySheet, "D2", fmt.Sprintf("D%d", summaryLast), ids["usd"]); err != nil {
		return nil, err
	}
	if err := f.SetCellStyle(tencentSummarySheet, "E2", fmt.Sprintf("E%d", summaryLast), ids["amount"]); err != nil {
		return nil, err
	}
	if err := f.SetCellStyle(tencentSummarySheet, "A1", "F1", ids["header"]); err != nil {
		return nil, err
	}

	for _, w := range []struct {
		sheet, from, to string
		width           float64
	}{
		{tencentSummarySheet, "A", "A", 22}, {tencentSummarySheet, "B", "C", 14}, {tencentSummarySheet, "D", "E", 22}, {tencentSummarySheet, "F", "F", 16},
		{tencentLinesSheet, "A", "A", 22}, {tencentLinesSheet, "B", "C", 14}, {tencentLinesSheet, "D", "D", 40}, {tencentLinesSheet, "E", "E", 12},
		{tencentLinesSheet, "F", "F", 40}, {tencentLinesSheet, "G", "G", 18}, {tencentLinesSheet, "H", "I", 10}, {tencentLinesSheet, "J", "J", 18},
		{tencentLinesSheet, "K", "K", 16}, {tencentLinesSheet, "L", "L", 16}, {tencentLinesSheet, "M", "M", 36}, {tencentLinesSheet, "N", "N", 16},
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
	rows, err := db.Query(`
		SELECT s.month, s.status, s.fx_rate, s.ref_fx_rate, s.generated_at, s.confirmed_at,
		       COUNT(l.line_key), COALESCE(SUM(l.official_usd), 0), COALESCE(SUM(l.invoice_rmb), 0)
		  FROM rs_tencent_statement s
		  LEFT JOIN rs_tencent_statement_line l ON l.month = s.month
		 GROUP BY s.month
		 ORDER BY s.month DESC`)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()
	type item struct {
		Month       string  `json:"month"`
		Status      string  `json:"status"`
		FxRate      float64 `json:"fx_rate"`
		RefFxRate   float64 `json:"ref_fx_rate"`
		GeneratedAt int64   `json:"generated_at"`
		ConfirmedAt int64   `json:"confirmed_at"`
		Lines       int     `json:"lines"`
		OfficialUSD float64 `json:"official_usd"`
		InvoiceRMB  float64 `json:"invoice_rmb"`
	}
	list := []item{}
	for rows.Next() {
		var it item
		if err := rows.Scan(&it.Month, &it.Status, &it.FxRate, &it.RefFxRate, &it.GeneratedAt, &it.ConfirmedAt,
			&it.Lines, &it.OfficialUSD, &it.InvoiceRMB); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		it.OfficialUSD = tencentRound(it.OfficialUSD, 2)
		it.InvoiceRMB = tencentRound(it.InvoiceRMB, 2)
		list = append(list, it)
	}
	c.JSON(http.StatusOK, gin.H{"list": list})
}

func handleTencentStatementGet(c *gin.Context) {
	month, ok := tencentSnapshotMonth(c)
	if !ok {
		return
	}
	h, lines, err := tencentLoadSnapshot(month.Format("2006-01"), c.Query("supplier"))
	if errors.Is(err, sql.ErrNoRows) {
		c.JSON(http.StatusNotFound, gin.H{"error": "该月尚未生成对账单"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"statement":       h,
		"headers":         tencentStatementHeaders,
		"summary_headers": tencentSummaryHeaders,
		"lines":           lines,
		"summary":         tencentSummarize(lines),
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
// supplier (?supplier=<column A>) or all suppliers in one file.
func handleTencentStatementExport(c *gin.Context) {
	month, ok := tencentSnapshotMonth(c)
	if !ok {
		return
	}
	supplier := strings.TrimSpace(c.Query("supplier"))
	_, lines, err := tencentLoadSnapshot(month.Format("2006-01"), supplier)
	if errors.Is(err, sql.ErrNoRows) {
		c.JSON(http.StatusNotFound, gin.H{"error": "该月尚未生成对账单"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if supplier != "" && len(lines) == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "该供应商在本月对账单中没有明细"})
		return
	}
	f, err := tencentStatementWorkbook(lines)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer f.Close()
	name := fmt.Sprintf("%s-%d月对账单.xlsx", cmp.Or(supplier, "全部供应商"), int(month.Month()))
	c.Header("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="statement-%s.xlsx"; filename*=UTF-8''%s`,
		month.Format("2006-01"), url.PathEscape(name)))
	if err := f.Write(c.Writer); err != nil {
		c.Status(http.StatusInternalServerError)
	}
}
