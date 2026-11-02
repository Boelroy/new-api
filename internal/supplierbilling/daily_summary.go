package supplierbilling

import (
	"cmp"
	"database/sql"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

type VendorDailyUsage struct {
	VendorID     int64  `json:"vendor_id"`
	VendorName   string `json:"vendor_name"`
	GatewayUsage string `json:"gateway_usage"`
	RequestCount string `json:"request_count"`
}

type HeadUsageTotal struct {
	SupplierID   int64  `json:"supplier_id"`
	SupplierName string `json:"supplier_name"`
	GatewayUsage string `json:"gateway_usage"`
	RequestCount string `json:"request_count"`
}

type HeadDailyUsage struct {
	HeadUsageTotal
	SourcesSynced bool               `json:"sources_synced"`
	Vendors       []VendorDailyUsage `json:"vendors"`
}

type DailyUsageSummary struct {
	Date         string           `json:"date"`
	GatewayUsage string           `json:"gateway_usage"`
	RequestCount string           `json:"request_count"`
	Heads        []HeadDailyUsage `json:"heads"`
}

type supplierUsageKey struct {
	Date       string
	SupplierID int64
	VendorID   int64
}

type supplierUsageAmount struct {
	Quantity decimal.Decimal
	Requests decimal.Decimal
}

// dailySummary presents the business hierarchy rather than channel/source rows.
// Quantities stay in gateway USD and are independent of settlement prices.
func (s *Server) dailySummary(w http.ResponseWriter, r *http.Request) {
	var selected int64
	if raw := r.URL.Query().Get("supplier_id"); raw != "" {
		var err error
		selected, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || selected < 0 {
			s.fail(w, 400, "Select a supplier")
			return
		}
	}
	user := currentUser(r)
	if user.Role == RoleSupplier {
		if selected != 0 && selected != user.SupplierID {
			s.fail(w, 403, "Permission denied")
			return
		}
		selected = user.SupplierID
	}
	if selected > 0 {
		if _, ok := s.targetSupplier(w, r, selected); !ok {
			return
		}
	}
	start, end, err := statementRange(r.URL.Query().Get("start"), r.URL.Query().Get("end"))
	if err != nil {
		s.fail(w, 400, err.Error())
		return
	}
	startText, endText := start.Format("2006-01-02"), end.Format("2006-01-02")
	heads := []Supplier{}
	usage := []Usage{}
	vendors := []Vendor{}
	sources := []Source{}
	coverage := []SourceSync{}
	var options *sql.TxOptions
	if s.DB.Dialector.Name() != "sqlite" {
		options = &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true}
	}
	err = s.DB.Transaction(func(tx *gorm.DB) error {
		query := tx.Order("id")
		if selected > 0 {
			query = query.Where("id = ?", selected)
		}
		if err := query.Find(&heads).Error; err != nil {
			return err
		}
		if len(heads) == 0 {
			return nil
		}
		ids := make([]int64, len(heads))
		for i, head := range heads {
			ids[i] = head.ID
		}
		if err := tx.Select("supplier_id", "vendor_id", "date", "quantity", "request_count").Where("supplier_id IN ? AND date >= ? AND date <= ? AND origin = ? AND unit = ?", ids, startText, endText, "sync", "gateway_usd").Find(&usage).Error; err != nil {
			return err
		}
		if err := tx.Where("supplier_id IN ?", ids).Find(&vendors).Error; err != nil {
			return err
		}
		if err := tx.Where("supplier_id IN ?", ids).Find(&sources).Error; err != nil {
			return err
		}
		if len(sources) == 0 {
			return nil
		}
		sourceIDs := make([]int64, len(sources))
		for i, source := range sources {
			sourceIDs[i] = source.ID
		}
		return tx.Where("source_id IN ? AND date >= ? AND date <= ?", sourceIDs, startText, endText).Find(&coverage).Error
	}, options)
	if err != nil {
		s.fail(w, 500, "Database operation failed")
		return
	}

	vendorsByID := make(map[int64]Vendor, len(vendors))
	for _, vendor := range vendors {
		vendorsByID[vendor.ID] = vendor
	}
	amounts := make(map[supplierUsageKey]supplierUsageAmount)
	for _, row := range usage {
		quantity, err := billingDecimal(row.Quantity, 1_000_000_000_000, true)
		if err != nil || row.RequestCount < 0 {
			s.fail(w, 500, "Invalid usage item")
			return
		}
		vendorID := row.VendorID
		vendor, known := vendorsByID[vendorID]
		if !known || vendor.SupplierID != row.SupplierID {
			// Legacy or missing metadata is explicit, never discarded or exposed
			// under another head's supplier name.
			vendorID = 0
		}
		key := supplierUsageKey{Date: row.Date, SupplierID: row.SupplierID, VendorID: vendorID}
		current := amounts[key]
		current.Quantity = current.Quantity.Add(quantity)
		current.Requests = current.Requests.Add(decimal.NewFromInt(row.RequestCount))
		amounts[key] = current
	}
	sourcesByHead := map[int64]int{}
	headBySource := map[int64]int64{}
	for _, source := range sources {
		sourcesByHead[source.SupplierID]++
		headBySource[source.ID] = source.SupplierID
	}
	covered := map[supplierUsageKey]int{}
	for _, row := range coverage {
		covered[supplierUsageKey{Date: row.Date, SupplierID: headBySource[row.SourceID]}]++
	}
	byDayHead := make(map[supplierUsageKey][]VendorDailyUsage)
	for key, amount := range amounts {
		name := "Unassigned supplier"
		if key.VendorID != 0 {
			name = vendorsByID[key.VendorID].Name
		}
		dayKey := supplierUsageKey{Date: key.Date, SupplierID: key.SupplierID}
		byDayHead[dayKey] = append(byDayHead[dayKey], VendorDailyUsage{VendorID: key.VendorID, VendorName: name, GatewayUsage: amount.Quantity.StringFixed(6), RequestCount: amount.Requests.StringFixed(0)})
	}
	totalsByHead := map[int64]supplierUsageAmount{}
	days := []DailyUsageSummary{}
	for day := start; !day.After(end); day = day.AddDate(0, 0, 1) {
		date := day.Format("2006-01-02")
		dayHeads := []HeadDailyUsage{}
		dayTotal := supplierUsageAmount{}
		for _, head := range heads {
			key := supplierUsageKey{Date: date, SupplierID: head.ID}
			children := byDayHead[key]
			if children == nil {
				children = []VendorDailyUsage{}
			}
			slices.SortFunc(children, func(a, b VendorDailyUsage) int {
				if order := strings.Compare(a.VendorName, b.VendorName); order != 0 {
					return order
				}
				return cmp.Compare(a.VendorID, b.VendorID)
			})
			current := supplierUsageAmount{}
			for _, child := range children {
				amount := amounts[supplierUsageKey{Date: date, SupplierID: head.ID, VendorID: child.VendorID}]
				current.Quantity = current.Quantity.Add(amount.Quantity)
				current.Requests = current.Requests.Add(amount.Requests)
			}
			headTotal := totalsByHead[head.ID]
			headTotal.Quantity = headTotal.Quantity.Add(current.Quantity)
			headTotal.Requests = headTotal.Requests.Add(current.Requests)
			totalsByHead[head.ID] = headTotal
			dayTotal.Quantity = dayTotal.Quantity.Add(current.Quantity)
			dayTotal.Requests = dayTotal.Requests.Add(current.Requests)
			dayHeads = append(dayHeads, HeadDailyUsage{HeadUsageTotal: HeadUsageTotal{SupplierID: head.ID, SupplierName: head.Name, GatewayUsage: current.Quantity.StringFixed(6), RequestCount: current.Requests.StringFixed(0)}, SourcesSynced: covered[key] == sourcesByHead[head.ID], Vendors: children})
		}
		days = append(days, DailyUsageSummary{Date: date, GatewayUsage: dayTotal.Quantity.StringFixed(6), RequestCount: dayTotal.Requests.StringFixed(0), Heads: dayHeads})
	}
	totals := []HeadUsageTotal{}
	for _, head := range heads {
		total := totalsByHead[head.ID]
		totals = append(totals, HeadUsageTotal{SupplierID: head.ID, SupplierName: head.Name, GatewayUsage: total.Quantity.StringFixed(6), RequestCount: total.Requests.StringFixed(0)})
	}
	s.write(w, 200, map[string]any{"timezone": "Asia/Shanghai", "start": startText, "end": endText, "heads": heads, "totals": totals, "days": days})
}
