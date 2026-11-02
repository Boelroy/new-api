package supplierbilling

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

var (
	decimalPattern  = regexp.MustCompile(`^[0-9]{1,13}(\.[0-9]{1,6})?$`)
	currencyPattern = regexp.MustCompile(`^[A-Z]{3}$`)
	errConflict     = errors.New("record changed")
)

// billingDecimal bounds all externally supplied multipliers before arithmetic.
// Scientific notation is deliberately rejected to prevent huge exponents.
func billingDecimal(raw string, max int64, allowZero bool) (decimal.Decimal, error) {
	if !decimalPattern.MatchString(raw) {
		return decimal.Zero, errors.New("Invalid decimal amount")
	}
	d, err := decimal.NewFromString(raw)
	if err != nil || d.IsNegative() || (!allowZero && d.IsZero()) || d.GreaterThan(decimal.NewFromInt(max)) {
		return decimal.Zero, errors.New("Amount is out of range")
	}
	return d, nil
}

// calculateCharge converts a priced quantity to the supplier's currency. A
// "divide" rate such as 6.7 CNY per USD has no exact decimal multiplier.
func calculateCharge(quantity, unitPrice, discount, fxRate, rateMode string) (string, error) {
	if rateMode != "multiply" && rateMode != "divide" {
		return "", errors.New("Invalid exchange rate mode")
	}
	q, err := billingDecimal(quantity, 1_000_000_000_000, true)
	if err != nil {
		return "", err
	}
	p, err := billingDecimal(unitPrice, 1_000_000, true)
	if err != nil {
		return "", err
	}
	d, err := billingDecimal(discount, 1, true)
	if err != nil {
		return "", err
	}
	f, err := billingDecimal(fxRate, 100_000, false)
	if err != nil {
		return "", err
	}
	amount := q.Mul(p).Mul(d).Mul(f).Round(6)
	if rateMode == "divide" {
		amount = q.Mul(p).Mul(d).Div(f).Round(6)
	}
	if amount.GreaterThan(decimal.NewFromInt(1_000_000_000_000)) {
		return "", errors.New("Amount is out of range")
	}
	return amount.StringFixed(6), nil
}

func digest(v any) string {
	b, _ := common.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func validDate(raw string) bool {
	d, err := time.ParseInLocation("2006-01-02", raw, businessZone)
	return err == nil && d.Format("2006-01-02") == raw && d.Year() >= 2000 && !d.After(time.Now().In(businessZone))
}

func (s *Server) targetSupplier(w http.ResponseWriter, r *http.Request, id int64) (Supplier, bool) {
	u := currentUser(r)
	if u.Role == RoleSupplier {
		if id != 0 && id != u.SupplierID {
			s.fail(w, 403, "Permission denied")
			return Supplier{}, false
		}
		id = u.SupplierID
	}
	if id <= 0 {
		s.fail(w, 400, "Select a supplier")
		return Supplier{}, false
	}
	var supplier Supplier
	if err := s.DB.First(&supplier, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			s.fail(w, 404, "Supplier not found")
		} else {
			s.fail(w, 500, "Database operation failed")
		}
		return Supplier{}, false
	}
	return supplier, true
}

func (s *Server) listSuppliers(w http.ResponseWriter, r *http.Request) {
	items := []Supplier{}
	query := s.DB.Order("id")
	if u := currentUser(r); u.Role == RoleSupplier {
		query = query.Where("id = ?", u.SupplierID)
	}
	if err := query.Find(&items).Error; err != nil {
		s.fail(w, 500, "Database operation failed")
		return
	}
	s.write(w, 200, items)
}

func (s *Server) createSupplier(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name     string `json:"name"`
		Currency string `json:"currency"`
	}
	if !s.decode(w, r, &body) {
		return
	}
	body.Name = strings.TrimSpace(body.Name)
	if body.Currency == "" {
		body.Currency = "CNY"
	}
	if body.Name == "" || len(body.Name) > 120 || !currencyPattern.MatchString(body.Currency) {
		s.fail(w, 400, "Invalid supplier")
		return
	}
	row := Supplier{Name: body.Name, Currency: body.Currency, CreatedAt: time.Now().Unix()}
	if err := s.DB.Create(&row).Error; err != nil {
		s.fail(w, 500, "Database operation failed")
		return
	}
	s.write(w, 201, row)
}

func (s *Server) createUsage(w http.ResponseWriter, r *http.Request) {
	var body struct {
		SupplierID     int64  `json:"supplier_id"`
		Date           string `json:"date"`
		UsageType      string `json:"usage_type"`
		Description    string `json:"description"`
		Quantity       string `json:"quantity"`
		Unit           string `json:"unit"`
		UnitPrice      string `json:"unit_price"`
		Currency       string `json:"currency"`
		Discount       string `json:"discount"`
		FxRate         string `json:"fx_rate"`
		RateMode       string `json:"rate_mode"`
		IdempotencyKey string `json:"idempotency_key"`
	}
	if !s.decode(w, r, &body) {
		return
	}
	supplier, ok := s.targetSupplier(w, r, body.SupplierID)
	if !ok {
		return
	}
	body.SupplierID = supplier.ID
	if body.RateMode == "" {
		body.RateMode = "multiply"
	}
	body.UsageType = strings.TrimSpace(body.UsageType)
	body.Unit = strings.TrimSpace(body.Unit)
	if !validDate(body.Date) || body.UsageType == "" || len(body.UsageType) > 120 || body.Unit == "" || len(body.Unit) > 64 || len(body.Description) > 2000 || !currencyPattern.MatchString(body.Currency) || len(body.IdempotencyKey) < 8 || len(body.IdempotencyKey) > 128 {
		s.fail(w, 400, "Invalid usage item")
		return
	}
	amount, err := calculateCharge(body.Quantity, body.UnitPrice, body.Discount, body.FxRate, body.RateMode)
	if err != nil {
		s.fail(w, 400, err.Error())
		return
	}
	fx, _ := decimal.NewFromString(body.FxRate)
	if body.Currency == supplier.Currency && !fx.Equal(decimal.NewFromInt(1)) {
		s.fail(w, 400, "Same-currency exchange rate must be 1")
		return
	}
	row := Usage{SupplierID: supplier.ID, Date: body.Date, UsageType: body.UsageType, Description: body.Description, Quantity: body.Quantity, Unit: body.Unit, UnitPrice: body.UnitPrice, Currency: body.Currency, Discount: body.Discount, FxRate: body.FxRate, RateMode: body.RateMode, Amount: amount, Origin: "manual", SourceName: "Manual", CreatedBy: currentUser(r).ID, CreatedAt: time.Now().Unix(), IdempotencyKey: digest([]any{"manual", supplier.ID, body.IdempotencyKey}), PayloadHash: digest(body)}
	err = s.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&row).Error; err != nil {
			return err
		}
		a, _ := decimal.NewFromString(amount)
		return tx.Create(&LedgerEntry{SupplierID: row.SupplierID, Date: row.Date, Kind: "usage", Amount: a.Neg().StringFixed(6), UsageID: row.ID, CreatedBy: row.CreatedBy, CreatedAt: row.CreatedAt}).Error
	})
	if err != nil {
		var existing Usage
		if s.DB.Where("idempotency_key = ?", row.IdempotencyKey).First(&existing).Error == nil {
			if existing.PayloadHash != row.PayloadHash {
				s.fail(w, 409, "Idempotency key was already used")
				return
			}
			s.write(w, 200, existing)
			return
		}
		s.fail(w, 500, "Database operation failed")
		return
	}
	s.write(w, 201, row)
}

func (s *Server) createRecharge(w http.ResponseWriter, r *http.Request) {
	var body struct {
		SupplierID     int64  `json:"supplier_id"`
		Date           string `json:"date"`
		Amount         string `json:"amount"`
		Reference      string `json:"reference"`
		Note           string `json:"note"`
		IdempotencyKey string `json:"idempotency_key"`
	}
	if !s.decode(w, r, &body) {
		return
	}
	supplier, ok := s.targetSupplier(w, r, body.SupplierID)
	if !ok {
		return
	}
	body.Reference = strings.TrimSpace(body.Reference)
	a, err := billingDecimal(body.Amount, 1_000_000_000_000, false)
	if err != nil || !validDate(body.Date) || body.Reference == "" || len(body.Reference) > 200 || len(body.Note) > 2000 || len(body.IdempotencyKey) < 8 || len(body.IdempotencyKey) > 128 {
		s.fail(w, 400, "Invalid recharge")
		return
	}
	row := Recharge{SupplierID: supplier.ID, Date: body.Date, Amount: a.StringFixed(6), Currency: supplier.Currency, Reference: body.Reference, Note: body.Note, CreatedBy: currentUser(r).ID, CreatedAt: time.Now().Unix(), IdempotencyKey: digest([]any{"recharge", supplier.ID, body.IdempotencyKey}), PayloadHash: digest(body)}
	err = s.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&row).Error; err != nil {
			return err
		}
		return tx.Create(&LedgerEntry{SupplierID: row.SupplierID, Date: row.Date, Kind: "recharge", Amount: row.Amount, RechargeID: row.ID, Note: row.Note, CreatedBy: row.CreatedBy, CreatedAt: row.CreatedAt}).Error
	})
	if err != nil {
		var existing Recharge
		if s.DB.Where("idempotency_key = ?", row.IdempotencyKey).First(&existing).Error == nil {
			if existing.PayloadHash != row.PayloadHash {
				s.fail(w, 409, "Idempotency key was already used")
				return
			}
			s.write(w, 200, existing)
			return
		}
		s.fail(w, 500, "Database operation failed")
		return
	}
	s.write(w, 201, row)
}

func (s *Server) reverseRecharge(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Note string `json:"note"`
	}
	if !s.decode(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.Note) == "" || len(body.Note) > 2000 {
		s.fail(w, 400, "A reversal reason is required")
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		s.fail(w, 400, "Invalid recharge")
		return
	}
	err = s.DB.Transaction(func(tx *gorm.DB) error {
		var row Recharge
		if err := tx.First(&row, id).Error; err != nil {
			return err
		}
		if row.Reversed {
			return nil
		}
		result := tx.Model(&Recharge{}).Where("id = ? AND reversed = ?", id, false).Update("reversed", true)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errConflict
		}
		a, err := decimal.NewFromString(row.Amount)
		if err != nil {
			return err
		}
		return tx.Create(&LedgerEntry{SupplierID: row.SupplierID, Date: time.Now().In(businessZone).Format("2006-01-02"), Kind: "recharge_reversal", Amount: a.Neg().StringFixed(6), RechargeID: row.ID, Note: body.Note, CreatedBy: currentUser(r).ID, CreatedAt: time.Now().Unix()}).Error
	})
	if errors.Is(err, gorm.ErrRecordNotFound) {
		s.fail(w, 404, "Recharge not found")
		return
	}
	if errors.Is(err, errConflict) {
		s.fail(w, 409, "Record changed; retry the request")
		return
	}
	if err != nil {
		s.fail(w, 500, "Database operation failed")
		return
	}
	s.write(w, 200, map[string]bool{"ok": true})
}

type DailyStatement struct {
	Date           string `json:"date"`
	Usage          string `json:"usage"`
	GatewayUsage   string `json:"gateway_usage"`
	UnpricedUsage  string `json:"unpriced_usage"`
	RequestCount   string `json:"request_count"`
	Recharges      string `json:"recharges"`
	Initial        string `json:"initial"`
	ClosingBalance string `json:"closing_balance"`
	SourcesSynced  bool   `json:"sources_synced"`
}

type StatementSourceStatus struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	LastSyncAt  int64  `json:"last_sync_at"`
	SyncFailed  bool   `json:"sync_failed"`
	MissingDays int    `json:"missing_days"`
}

func statementRange(startRaw, endRaw string) (time.Time, time.Time, error) {
	start, err := time.ParseInLocation("2006-01-02", startRaw, businessZone)
	if err != nil {
		return time.Time{}, time.Time{}, errors.New("Invalid date range")
	}
	end, err := time.ParseInLocation("2006-01-02", endRaw, businessZone)
	if err != nil || end.Before(start) || end.Sub(start) > 365*24*time.Hour || start.Year() < 2000 {
		return time.Time{}, time.Time{}, errors.New("Date range must be within 366 days")
	}
	return start, end, nil
}
