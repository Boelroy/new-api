package supplierbilling

import (
	"cmp"
	"database/sql"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

// StatementLine is one row of the supplier settlement sheet: a priced product
// line for a day, or a balance movement. Balance is the running balance after it.
type StatementLine struct {
	Date             string `json:"date"`
	Kind             string `json:"kind"`
	UsageType        string `json:"usage_type"`
	Description      string `json:"description"`
	Origin           string `json:"origin"`
	Quantity         string `json:"quantity"`
	Unit             string `json:"unit"`
	UnitPrice        string `json:"unit_price"`
	Currency         string `json:"currency"`
	Discount         string `json:"discount"`
	FxRate           string `json:"fx_rate"`
	RateMode         string `json:"rate_mode"`
	SettlementStatus string `json:"settlement_status"`
	OriginalAmount   string `json:"original_amount"`
	Change           string `json:"change"`
	Balance          string `json:"balance"`
	Note             string `json:"note"`
	RechargeID       int64  `json:"recharge_id"`
	Claim            *Claim `json:"claim"`
}

type SupplierStatement struct {
	Supplier       Supplier                `json:"supplier"`
	Balance        string                  `json:"balance"`
	OpeningBalance string                  `json:"opening_balance"`
	InitialBalance *InitialBalance         `json:"initial_balance"`
	TotalRecharges string                  `json:"total_recharges"`
	TotalUsage     string                  `json:"total_usage"`
	GatewayUsage   string                  `json:"gateway_usage"`
	UnpricedUsage  string                  `json:"unpriced_usage"`
	Days           []DailyStatement        `json:"days"`
	SourceStatus   []StatementSourceStatus `json:"source_status"`
	Lines          []StatementLine         `json:"lines,omitempty"`
	Usage          []Usage                 `json:"usage,omitempty"`
	Recharges      []Recharge              `json:"recharges,omitempty"`
	Ledger         []LedgerEntry           `json:"ledger,omitempty"`
	Timezone       string                  `json:"timezone"`
}

type statementLineKey struct {
	date, usageType, unit, unitPrice, currency, discount, fxRate, rateMode, status string
	manualID                                                                       int64
}

var lineKindOrder = map[string]int{"initial_balance": 0, "initial_balance_reversal": 0, "usage": 1, "claim": 1}

// buildStatement computes balances from the full ledger. Details adds the
// settlement sheet and raw rows; the overview only needs daily totals.
func (s *Server) buildStatement(supplier Supplier, start, end time.Time, details bool) (SupplierStatement, error) {
	startText, endText := start.Format("2006-01-02"), end.Format("2006-01-02")
	entries := []LedgerEntry{}
	usage := []Usage{}
	recharges := []Recharge{}
	sources := []Source{}
	coverage := []SourceSync{}
	claims := []Claim{}
	initial := []InitialBalance{}
	// PostgreSQL's default READ COMMITTED uses a different snapshot per SELECT.
	// Request repeatable read for server databases; SQLite snapshots on first read.
	var options *sql.TxOptions
	if s.DB.Dialector.Name() != "sqlite" {
		options = &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true}
	}
	err := s.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("supplier_id = ?", supplier.ID).Order("date, id").Find(&entries).Error; err != nil {
			return err
		}
		usageQuery := tx.Where("supplier_id = ? AND date >= ? AND date <= ?", supplier.ID, startText, endText).Order("date DESC, id DESC")
		if !details {
			usageQuery = usageQuery.Select("date", "origin", "unit", "quantity", "request_count", "settlement_status", "usage_type", "amount")
		}
		if err := usageQuery.Find(&usage).Error; err != nil {
			return err
		}
		if err := tx.Where("supplier_id = ? AND date >= ? AND date <= ?", supplier.ID, startText, endText).Order("date DESC, id DESC").Find(&recharges).Error; err != nil {
			return err
		}
		if err := tx.Where("supplier_id = ? AND date >= ? AND date <= ?", supplier.ID, startText, endText).Find(&claims).Error; err != nil {
			return err
		}
		if err := tx.Where("supplier_id = ?", supplier.ID).Order("revision DESC").Limit(1).Find(&initial).Error; err != nil {
			return err
		}
		if err := tx.Where("supplier_id = ?", supplier.ID).Find(&sources).Error; err != nil {
			return err
		}
		ids := make([]int64, len(sources))
		for i, source := range sources {
			ids[i] = source.ID
		}
		if len(ids) == 0 {
			return nil
		}
		return tx.Where("source_id IN ? AND date >= ? AND date <= ?", ids, startText, endText).Find(&coverage).Error
	}, options)
	if err != nil {
		return SupplierStatement{}, errors.New("Database operation failed")
	}
	balance, opening, totalUsage, totalRecharges := decimal.Zero, decimal.Zero, decimal.Zero, decimal.Zero
	dayUsage, dayRecharges, dayInitial := map[string]decimal.Decimal{}, map[string]decimal.Decimal{}, map[string]decimal.Decimal{}
	gatewayUsage, unpricedUsage := decimal.Zero, decimal.Zero
	dayGateway, dayUnpriced, dayRequests := map[string]decimal.Decimal{}, map[string]decimal.Decimal{}, map[string]decimal.Decimal{}
	type productTotals struct{ quantity, unpriced, amount decimal.Decimal }
	type productKey struct{ date, usageType, unit string }
	products := map[productKey]*productTotals{}
	productOrder := map[string][]productKey{}
	productRequests := map[productKey]int64{}
	for _, item := range usage {
		quantity, quantityErr := decimal.NewFromString(item.Quantity)
		amount, amountErr := decimal.NewFromString(item.Amount)
		if quantityErr != nil || amountErr != nil {
			return SupplierStatement{}, errors.New("Invalid usage item")
		}
		key := productKey{item.Date, item.UsageType, item.Unit}
		current, exists := products[key]
		if !exists {
			current = &productTotals{}
			products[key] = current
			productOrder[item.Date] = append(productOrder[item.Date], key)
		}
		current.quantity = current.quantity.Add(quantity)
		current.amount = current.amount.Add(amount)
		if item.SettlementStatus == "unpriced" {
			current.unpriced = current.unpriced.Add(quantity)
		}
		productRequests[key] += item.RequestCount
	}
	for _, item := range usage {
		if item.Origin != "sync" || item.Unit != "gateway_usd" {
			continue
		}
		quantity, err := billingDecimal(item.Quantity, 1_000_000_000_000, true)
		if err != nil || item.RequestCount < 0 {
			return SupplierStatement{}, errors.New("Invalid usage item")
		}
		gatewayUsage = gatewayUsage.Add(quantity)
		dayGateway[item.Date] = dayGateway[item.Date].Add(quantity)
		dayRequests[item.Date] = dayRequests[item.Date].Add(decimal.NewFromInt(item.RequestCount))
		if item.SettlementStatus == "unpriced" {
			unpricedUsage = unpricedUsage.Add(quantity)
			dayUnpriced[item.Date] = dayUnpriced[item.Date].Add(quantity)
		}
	}
	visibleEntries := []LedgerEntry{}
	for _, entry := range entries {
		amount, err := decimal.NewFromString(entry.Amount)
		if err != nil {
			return SupplierStatement{}, errors.New("Invalid ledger amount")
		}
		balance = balance.Add(amount)
		if entry.Date < startText {
			opening = opening.Add(amount)
		}
		if entry.Date < startText || entry.Date > endText {
			continue
		}
		visibleEntries = append(visibleEntries, entry)
		switch entry.Kind {
		case "recharge", "recharge_reversal":
			totalRecharges = totalRecharges.Add(amount)
			dayRecharges[entry.Date] = dayRecharges[entry.Date].Add(amount)
		case "initial_balance", "initial_balance_reversal":
			dayInitial[entry.Date] = dayInitial[entry.Date].Add(amount)
		default:
			totalUsage = totalUsage.Sub(amount)
			dayUsage[entry.Date] = dayUsage[entry.Date].Sub(amount)
		}
	}
	days := []DailyStatement{}
	coveredDays := map[string]int{}
	coveredSources := map[int64]int{}
	for _, row := range coverage {
		coveredDays[row.Date]++
		coveredSources[row.SourceID]++
	}
	closing := opening
	for day := start; !day.After(end); day = day.AddDate(0, 0, 1) {
		date := day.Format("2006-01-02")
		closing = closing.Add(dayInitial[date]).Add(dayRecharges[date]).Sub(dayUsage[date])
		dayProducts := []DailyProduct{}
		for _, key := range productOrder[date] {
			totals := products[key]
			dayProducts = append(dayProducts, DailyProduct{UsageType: key.usageType, Unit: key.unit, Quantity: totals.quantity.StringFixed(6), Unpriced: totals.unpriced.StringFixed(6), Amount: totals.amount.StringFixed(6), RequestCount: productRequests[key]})
		}
		slices.SortFunc(dayProducts, func(a, b DailyProduct) int {
			return cmp.Or(strings.Compare(a.UsageType, b.UsageType), strings.Compare(a.Unit, b.Unit))
		})
		days = append(days, DailyStatement{Products: dayProducts, Date: date, Usage: dayUsage[date].StringFixed(6), GatewayUsage: dayGateway[date].StringFixed(6), UnpricedUsage: dayUnpriced[date].StringFixed(6), RequestCount: dayRequests[date].StringFixed(0), Recharges: dayRecharges[date].StringFixed(6), Initial: dayInitial[date].StringFixed(6), ClosingBalance: closing.StringFixed(6), SourcesSynced: coveredDays[date] == len(sources)})
	}
	statuses := []StatementSourceStatus{}
	for _, source := range sources {
		statuses = append(statuses, StatementSourceStatus{ID: source.ID, Name: source.Name, LastSyncAt: source.LastSyncAt, SyncFailed: source.LastError != "", MissingDays: len(days) - coveredSources[source.ID]})
	}
	result := SupplierStatement{Supplier: supplier, Balance: balance.StringFixed(6), OpeningBalance: opening.StringFixed(6), TotalRecharges: totalRecharges.StringFixed(6), TotalUsage: totalUsage.StringFixed(6), GatewayUsage: gatewayUsage.StringFixed(6), UnpricedUsage: unpricedUsage.StringFixed(6), Days: days, SourceStatus: statuses, Timezone: "Asia/Shanghai"}
	if len(initial) == 1 {
		result.InitialBalance = &initial[0]
	}
	if !details {
		return result, nil
	}
	lines, err := settlementLines(usage, visibleEntries, claims, opening)
	if err != nil {
		return SupplierStatement{}, err
	}
	result.Lines, result.Usage, result.Recharges, result.Ledger = lines, usage, recharges, visibleEntries
	return result, nil
}

// settlementLines groups imported rows by day, product line and price snapshot,
// so each sheet row has one unit price. Usage charges come from the rows' current
// amounts; their ledger postings (charges and corrections) net to the same value.
func settlementLines(usage []Usage, entries []LedgerEntry, claims []Claim, opening decimal.Decimal) ([]StatementLine, error) {
	lines := []StatementLine{}
	type lineTotals struct{ quantity, original, amount decimal.Decimal }
	totals := []lineTotals{}
	index := map[statementLineKey]int{}
	for _, item := range usage {
		key := statementLineKey{date: item.Date, usageType: item.UsageType, unit: item.Unit, unitPrice: item.UnitPrice, currency: item.Currency, discount: item.Discount, fxRate: item.FxRate, rateMode: item.RateMode, status: item.SettlementStatus}
		if item.Origin != "sync" {
			key.manualID = item.ID
		}
		position, exists := index[key]
		if !exists {
			position = len(lines)
			index[key] = position
			lines = append(lines, StatementLine{Date: item.Date, Kind: "usage", UsageType: item.UsageType, Origin: item.Origin, Unit: item.Unit, UnitPrice: item.UnitPrice, Currency: item.Currency, Discount: item.Discount, FxRate: item.FxRate, RateMode: item.RateMode, SettlementStatus: item.SettlementStatus})
			if item.Origin != "sync" {
				lines[position].Description = item.Description
			}
			totals = append(totals, lineTotals{})
		}
		quantity, err := decimal.NewFromString(item.Quantity)
		if err != nil {
			return nil, errors.New("Invalid usage item")
		}
		amount, err := decimal.NewFromString(item.Amount)
		if err != nil {
			return nil, errors.New("Invalid usage item")
		}
		totals[position].quantity = totals[position].quantity.Add(quantity)
		totals[position].amount = totals[position].amount.Add(amount)
		if item.SettlementStatus != "unpriced" {
			price, priceErr := decimal.NewFromString(item.UnitPrice)
			discount, discountErr := decimal.NewFromString(item.Discount)
			if priceErr != nil || discountErr != nil {
				return nil, errors.New("Invalid usage item")
			}
			totals[position].original = totals[position].original.Add(quantity.Mul(price).Mul(discount).Round(6))
		}
	}
	for i := range lines {
		lines[i].Quantity = totals[i].quantity.StringFixed(6)
		lines[i].OriginalAmount = totals[i].original.StringFixed(6)
		lines[i].Change = totals[i].amount.Neg().StringFixed(6)
	}
	// A claim attaches to the first line of its product line and day; a claim
	// without measured usage still appears so the gap is visible.
	for i := range claims {
		claim := &claims[i]
		attached := false
		for j := range lines {
			if lines[j].Kind == "usage" && lines[j].Date == claim.Date && lines[j].UsageType == claim.UsageType && lines[j].Claim == nil {
				lines[j].Claim, attached = claim, true
				break
			}
		}
		if !attached {
			lines = append(lines, StatementLine{Date: claim.Date, Kind: "claim", UsageType: claim.UsageType, Change: "0.000000", Claim: claim})
		}
	}
	for _, entry := range entries {
		if entry.Kind == "usage" || entry.Kind == "usage_adjustment" || entry.Kind == "adjustment" {
			continue
		}
		lines = append(lines, StatementLine{Date: entry.Date, Kind: entry.Kind, Change: entry.Amount, Note: entry.Note, RechargeID: entry.RechargeID})
	}
	slices.SortStableFunc(lines, func(a, b StatementLine) int {
		if order := strings.Compare(a.Date, b.Date); order != 0 {
			return order
		}
		rankA, knownA := lineKindOrder[a.Kind]
		rankB, knownB := lineKindOrder[b.Kind]
		if !knownA {
			rankA = 2
		}
		if !knownB {
			rankB = 2
		}
		if order := cmp.Compare(rankA, rankB); order != 0 {
			return order
		}
		return strings.Compare(a.UsageType, b.UsageType)
	})
	running := opening
	for i := range lines {
		change, err := decimal.NewFromString(lines[i].Change)
		if err != nil {
			return nil, errors.New("Invalid ledger amount")
		}
		running = running.Add(change)
		lines[i].Balance = running.StringFixed(6)
	}
	return lines, nil
}

func (s *Server) statement(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.URL.Query().Get("supplier_id"), 10, 64)
	supplier, ok := s.targetSupplier(w, r, id)
	if !ok {
		return
	}
	start, end, err := statementRange(r.URL.Query().Get("start"), r.URL.Query().Get("end"))
	if err != nil {
		s.fail(w, 400, err.Error())
		return
	}
	result, err := s.buildStatement(supplier, start, end, true)
	if err != nil {
		s.fail(w, 500, err.Error())
		return
	}
	if result.Usage == nil {
		result.Usage = []Usage{}
	}
	if result.Recharges == nil {
		result.Recharges = []Recharge{}
	}
	if result.Ledger == nil {
		result.Ledger = []LedgerEntry{}
	}
	s.write(w, 200, result)
}

// overview gives managers every supplier's daily payable and balance at once.
func (s *Server) overview(w http.ResponseWriter, r *http.Request) {
	start, end, err := statementRange(r.URL.Query().Get("start"), r.URL.Query().Get("end"))
	if err != nil {
		s.fail(w, 400, err.Error())
		return
	}
	suppliers := []Supplier{}
	if err := s.DB.Order("id").Find(&suppliers).Error; err != nil {
		s.fail(w, 500, "Database operation failed")
		return
	}
	items := make([]SupplierStatement, 0, len(suppliers))
	for _, supplier := range suppliers {
		item, err := s.buildStatement(supplier, start, end, false)
		if err != nil {
			s.fail(w, 500, err.Error())
			return
		}
		items = append(items, item)
	}
	s.write(w, 200, map[string]any{"suppliers": items, "timezone": "Asia/Shanghai"})
}

func (s *Server) setInitialBalance(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		s.fail(w, 400, "Select a supplier")
		return
	}
	var body struct {
		Date     string `json:"date"`
		Amount   string `json:"amount"`
		Note     string `json:"note"`
		Revision int64  `json:"revision"`
	}
	if !s.decode(w, r, &body) {
		return
	}
	supplier, ok := s.targetSupplier(w, r, id)
	if !ok {
		return
	}
	// A negative initial balance is an amount already owed to the supplier.
	amount, err := billingDecimal(strings.TrimPrefix(body.Amount, "-"), 1_000_000_000_000, true)
	if strings.HasPrefix(body.Amount, "-") {
		amount = amount.Neg()
	}
	if err != nil || !validDate(body.Date) || len(body.Note) > 2000 || body.Revision < 0 {
		s.fail(w, 400, "Invalid initial balance")
		return
	}
	actor, now := currentUser(r).ID, time.Now().Unix()
	row := InitialBalance{SupplierID: supplier.ID, Revision: body.Revision + 1, Date: body.Date, Amount: amount.StringFixed(6), Note: body.Note, CreatedBy: actor, CreatedAt: now}
	err = s.DB.Transaction(func(tx *gorm.DB) error {
		current := []InitialBalance{}
		if err := tx.Where("supplier_id = ?", supplier.ID).Order("revision DESC").Limit(1).Find(&current).Error; err != nil {
			return err
		}
		if (len(current) == 0 && body.Revision != 0) || (len(current) == 1 && current[0].Revision != body.Revision) {
			return errConflict
		}
		// The unique revision rejects a concurrent edit that read the same revision.
		if err := tx.Create(&row).Error; err != nil {
			return err
		}
		if len(current) == 1 {
			previous, err := decimal.NewFromString(current[0].Amount)
			if err != nil {
				return err
			}
			// The correction keeps the original date, so earlier balances reflect
			// the corrected opening amount while the ledger keeps both postings.
			if err := tx.Create(&LedgerEntry{SupplierID: supplier.ID, Date: current[0].Date, Kind: "initial_balance_reversal", Amount: previous.Neg().StringFixed(6), Note: "Initial balance replaced", CreatedBy: actor, CreatedAt: now}).Error; err != nil {
				return err
			}
		}
		return tx.Create(&LedgerEntry{SupplierID: supplier.ID, Date: row.Date, Kind: "initial_balance", Amount: row.Amount, Note: row.Note, CreatedBy: actor, CreatedAt: now}).Error
	})
	if err != nil {
		latest := []InitialBalance{}
		if errors.Is(err, errConflict) || (s.DB.Where("supplier_id = ?", supplier.ID).Order("revision DESC").Limit(1).Find(&latest).Error == nil && len(latest) == 1 && latest[0].Revision != body.Revision) {
			s.fail(w, 409, "Initial balance changed; reload and retry")
			return
		}
		s.fail(w, 500, "Database operation failed")
		return
	}
	s.write(w, 200, row)
}

// saveClaim records or clears the supplier's sheet figures for one line and day.
func (s *Server) saveClaim(w http.ResponseWriter, r *http.Request) {
	var body struct {
		SupplierID int64  `json:"supplier_id"`
		Date       string `json:"date"`
		UsageType  string `json:"usage_type"`
		Quantity   string `json:"quantity"`
		Amount     string `json:"amount"`
		Note       string `json:"note"`
		Delete     bool   `json:"delete"`
	}
	if !s.decode(w, r, &body) {
		return
	}
	supplier, ok := s.targetSupplier(w, r, body.SupplierID)
	if !ok {
		return
	}
	body.UsageType = strings.TrimSpace(body.UsageType)
	if !validDate(body.Date) || body.UsageType == "" || len(body.UsageType) > 120 || len(body.Note) > 2000 {
		s.fail(w, 400, "Invalid supplier claim")
		return
	}
	where := s.DB.Where("supplier_id = ? AND date = ? AND usage_type = ?", supplier.ID, body.Date, body.UsageType)
	if body.Delete {
		if err := where.Delete(&Claim{}).Error; err != nil {
			s.fail(w, 500, "Database operation failed")
			return
		}
		s.write(w, 200, map[string]bool{"ok": true})
		return
	}
	quantity, quantityErr := billingDecimal(body.Quantity, 1_000_000_000_000, true)
	amount, amountErr := billingDecimal(body.Amount, 1_000_000_000_000, true)
	if quantityErr != nil || amountErr != nil {
		s.fail(w, 400, "Invalid supplier claim")
		return
	}
	row := Claim{SupplierID: supplier.ID, Date: body.Date, UsageType: body.UsageType, Quantity: quantity.StringFixed(6), Amount: amount.StringFixed(6), Note: body.Note, CreatedBy: currentUser(r).ID, CreatedAt: time.Now().Unix()}
	err := s.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("supplier_id = ? AND date = ? AND usage_type = ?", supplier.ID, body.Date, body.UsageType).Delete(&Claim{}).Error; err != nil {
			return err
		}
		return tx.Create(&row).Error
	})
	if err != nil {
		s.fail(w, 409, "Record changed; retry the request")
		return
	}
	s.write(w, 200, row)
}

// priceUsage applies one sheet price to every imported row of a product line
// and day. Re-pricing settled rows appends the difference as a correction.
func (s *Server) priceUsage(w http.ResponseWriter, r *http.Request) {
	var body struct {
		SupplierID int64  `json:"supplier_id"`
		Date       string `json:"date"`
		UsageType  string `json:"usage_type"`
		UnitPrice  string `json:"unit_price"`
		Currency   string `json:"currency"`
		Discount   string `json:"discount"`
		FxRate     string `json:"fx_rate"`
	}
	if !s.decode(w, r, &body) {
		return
	}
	supplier, ok := s.targetSupplier(w, r, body.SupplierID)
	if !ok {
		return
	}
	if body.Discount == "" {
		body.Discount = "1"
	}
	body.UsageType = strings.TrimSpace(body.UsageType)
	if !validDate(body.Date) || body.UsageType == "" || !currencyPattern.MatchString(body.Currency) {
		s.fail(w, 400, "Invalid usage price")
		return
	}
	if _, err := calculateCharge("1", body.UnitPrice, body.Discount, body.FxRate, "divide"); err != nil {
		s.fail(w, 400, err.Error())
		return
	}
	fx, _ := decimal.NewFromString(body.FxRate)
	if body.Currency == supplier.Currency && !fx.Equal(decimal.NewFromInt(1)) {
		s.fail(w, 400, "Same-currency exchange rate must be 1")
		return
	}
	actor, now := currentUser(r).ID, time.Now().Unix()
	priced := 0
	err := s.DB.Transaction(func(tx *gorm.DB) error {
		// Sync takes the same source-version write first, so pricing and import
		// of the same rows serialize without dialect-specific row locks.
		if err := tx.Model(&Source{}).Where("supplier_id = ?", supplier.ID).Update("version", gorm.Expr("version + 1")).Error; err != nil {
			return err
		}
		rows := []Usage{}
		if err := tx.Where("supplier_id = ? AND date = ? AND usage_type = ? AND origin = ?", supplier.ID, body.Date, body.UsageType, "sync").Order("id").Find(&rows).Error; err != nil {
			return err
		}
		if len(rows) == 0 {
			return gorm.ErrRecordNotFound
		}
		for _, row := range rows {
			if row.SettlementStatus == "settled" && row.UnitPrice == body.UnitPrice && row.Currency == body.Currency && row.Discount == body.Discount && row.FxRate == body.FxRate && row.RateMode == "divide" {
				continue
			}
			amount, err := calculateCharge(row.Quantity, body.UnitPrice, body.Discount, body.FxRate, "divide")
			if err != nil {
				return err
			}
			previous, err := decimal.NewFromString(row.Amount)
			if err != nil {
				return err
			}
			next, _ := decimal.NewFromString(amount)
			if err := tx.Model(&Usage{}).Where("id = ?", row.ID).Updates(map[string]any{"unit_price": body.UnitPrice, "currency": body.Currency, "discount": body.Discount, "fx_rate": body.FxRate, "rate_mode": "divide", "amount": amount, "settlement_status": "settled"}).Error; err != nil {
				return err
			}
			kind, note := "usage", ""
			if row.SettlementStatus == "settled" {
				kind, note = "usage_adjustment", "Price correction"
			}
			if delta := previous.Sub(next); !delta.IsZero() {
				if err := tx.Create(&LedgerEntry{SupplierID: supplier.ID, Date: row.Date, Kind: kind, Amount: delta.StringFixed(6), UsageID: row.ID, Note: note, CreatedBy: actor, CreatedAt: now}).Error; err != nil {
					return err
				}
			}
			priced++
		}
		return nil
	})
	if errors.Is(err, gorm.ErrRecordNotFound) {
		s.fail(w, 404, "No imported usage for this line")
		return
	}
	if err != nil {
		s.fail(w, 500, "Database operation failed")
		return
	}
	s.write(w, 200, map[string]any{"ok": true, "priced": priced})
}
