package supplierbilling

import (
	"errors"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	credentialNamePattern = regexp.MustCompile(`^SUPPLIER_BILLING_SOURCE_[A-Z0-9_]+$`)
	errMappingOverlap     = errors.New("Source mapping overlaps another source")
)

var sourceKinds = []string{"report", "report_group", "remote_tag", "ccmax"}

// validateSourceEndpoint canonicalizes the upstream URL and checks the server
// allowlist and credential name shared by every source kind.
func (s *Server) validateSourceEndpoint(source *Source) error {
	if !slices.Contains(sourceKinds, source.Kind) || !credentialNamePattern.MatchString(source.CredentialEnv) || len(source.CredentialEnv) > 120 || source.GatewayUserID < 0 || source.GatewayUserID > maxGatewayUserID || source.ProfileID < 0 {
		return errors.New("Invalid source configuration")
	}
	u, err := url.Parse(source.BaseURL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" || len(source.BaseURL) > 2048 {
		return errors.New("Invalid source URL")
	}
	u.Host = strings.ToLower(u.Host)
	// Match the adapter's decoded URL, so encoded aliases cannot register a
	// second binding to the same upstream channel and charge its usage twice.
	u.RawPath = ""
	u.ForceQuery = false
	u.Path = path.Clean("/" + u.Path)
	if u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1")) {
		return errors.New("Source URL must use HTTPS")
	}
	if !slices.Contains(s.SourceOrigins, u.Scheme+"://"+u.Host) {
		return errors.New("Source origin is not allowed by the server")
	}
	source.BaseURL = strings.TrimRight(u.String(), "/")
	return nil
}

// validateSource normalizes a source and returns the binding keys that make
// each upstream channel, group, tag or uploader billable to only one source.
func (s *Server) validateSource(source *Source) ([]string, error) {
	source.Name = strings.TrimSpace(source.Name)
	source.UsageType = strings.TrimSpace(source.UsageType)
	if source.SettlementMode == "" {
		source.SettlementMode = "settlement"
	}
	if source.SettlementMode != "settlement" && source.SettlementMode != "usage_only" {
		return nil, errors.New("Invalid source configuration")
	}
	currencyAllowed := source.Currency == "USD" || (source.Kind == "ccmax" && source.Currency == "CNY")
	if source.Name == "" || len(source.Name) > 120 || source.UsageType == "" || len(source.UsageType) > 120 || !currencyAllowed {
		return nil, errors.New("Invalid source configuration")
	}
	if err := s.validateSourceEndpoint(source); err != nil {
		return nil, err
	}
	if source.SettlementMode == "usage_only" {
		// Missing settlement prices stay missing; zero and guessed rates must
		// not make an unpriced import appear to be an agreed free settlement.
		source.Discount = ""
		source.FxRate = ""
	} else {
		if _, err := billingDecimal(source.Discount, 1, true); err != nil {
			return nil, err
		}
		if _, err := billingDecimal(source.FxRate, 100_000, false); err != nil {
			return nil, err
		}
	}
	if source.Kind != "report" {
		source.ChannelIDs = ""
		if source.Kind != "remote_tag" {
			source.ExcludeSelector = false
			source.ProfileID = 0
		}
		if source.Kind == "remote_tag" && source.ProfileID <= 0 {
			return nil, errors.New("Select a remote profile")
		}
		values := []string{}
		for raw := range strings.SplitSeq(source.Selector, ",") {
			value := strings.TrimSpace(raw)
			if value == "" || slices.Contains(values, value) {
				continue
			}
			if len(value) > 120 {
				return nil, errors.New("Invalid source mapping")
			}
			values = append(values, value)
		}
		if len(values) > 500 || (len(values) == 0 && !source.ExcludeSelector) {
			return nil, errors.New("Select between 1 and 500 mapped values")
		}
		slices.Sort(values)
		source.Selector = strings.Join(values, ",")
		if source.ExcludeSelector {
			return []string{digest([]any{source.BaseURL, "tag-rest", source.ProfileID})}, nil
		}
		keys := make([]string, len(values))
		for i, value := range values {
			// A group binds regardless of the gateway user filter, so two user
			// scopes can never bill the same group twice.
			switch source.Kind {
			case "report_group":
				keys[i] = digest([]any{source.BaseURL, "group", value})
			case "remote_tag":
				keys[i] = digest([]any{source.BaseURL, "tag", source.ProfileID, value})
			default:
				keys[i] = digest([]any{source.BaseURL, "ccmax", value})
			}
		}
		return keys, nil
	}
	source.Selector, source.ExcludeSelector, source.ProfileID = "", false, 0
	channels := []int64{}
	for raw := range strings.SplitSeq(source.ChannelIDs, ",") {
		id, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
		if err != nil || id <= 0 || slices.Contains(channels, id) {
			return nil, errors.New("Enter unique positive channel IDs")
		}
		channels = append(channels, id)
	}
	if len(channels) == 0 || len(channels) > 500 {
		return nil, errors.New("Select between 1 and 500 channels")
	}
	slices.Sort(channels)
	parts := make([]string, len(channels))
	keys := make([]string, len(channels))
	for i, id := range channels {
		parts[i] = strconv.FormatInt(id, 10)
		keys[i] = digest([]any{source.BaseURL, id})
	}
	source.ChannelIDs = strings.Join(parts, ",")
	return keys, nil
}

// mapsValue reports whether an imported value belongs to this source's mapping.
func (source Source) mapsValue(value string) bool {
	if source.Kind == "report" {
		return true
	}
	listed := slices.Contains(strings.Split(source.Selector, ","), value)
	return listed != source.ExcludeSelector
}

// bindSource replaces a source's bindings inside tx. Channel and group sources
// on one upstream cannot be mixed, because a channel's group is not known here;
// an "all other tags" source must exclude every tag mapped elsewhere.
func bindSource(tx *gorm.DB, source Source, keys []string) error {
	others := []Source{}
	if err := tx.Where("base_url = ? AND id <> ?", source.BaseURL, source.ID).Find(&others).Error; err != nil {
		return err
	}
	for _, other := range others {
		if other.BaseURL != source.BaseURL {
			continue
		}
		if (source.Kind == "report" && other.Kind == "report_group") || (source.Kind == "report_group" && other.Kind == "report") {
			return errMappingOverlap
		}
		if source.Kind != "remote_tag" || other.Kind != "remote_tag" || other.ProfileID != source.ProfileID || source.ExcludeSelector == other.ExcludeSelector {
			continue
		}
		rest, mapped := source, other
		if other.ExcludeSelector {
			rest, mapped = other, source
		}
		for value := range strings.SplitSeq(mapped.Selector, ",") {
			if rest.mapsValue(value) {
				return errMappingOverlap
			}
		}
	}
	if err := tx.Where("source_id = ?", source.ID).Delete(&SourceChannel{}).Error; err != nil {
		return err
	}
	for _, key := range keys {
		if err := tx.Create(&SourceChannel{SourceID: source.ID, BindingKey: key}).Error; err != nil {
			return errMappingOverlap
		}
	}
	return nil
}

func (s *Server) listSources(w http.ResponseWriter, r *http.Request) {
	rows := []Source{}
	if err := s.DB.Order("id").Find(&rows).Error; err != nil {
		s.fail(w, 500, "Database operation failed")
		return
	}
	s.write(w, 200, rows)
}

func (s *Server) createSource(w http.ResponseWriter, r *http.Request) {
	var body Source
	if !s.decode(w, r, &body) {
		return
	}
	keys, err := s.validateSource(&body)
	if err != nil {
		s.fail(w, 400, err.Error())
		return
	}
	supplier, ok := s.targetSupplier(w, r, body.SupplierID)
	if !ok {
		return
	}
	if body.SettlementMode == "settlement" {
		fx, _ := decimal.NewFromString(body.FxRate)
		if supplier.Currency == body.Currency && !fx.Equal(decimal.NewFromInt(1)) {
			s.fail(w, 400, "Same-currency exchange rate must be 1")
			return
		}
	}
	row := Source{SupplierID: supplier.ID, Name: body.Name, Kind: body.Kind, BaseURL: body.BaseURL, CredentialEnv: body.CredentialEnv, ChannelIDs: body.ChannelIDs, Selector: body.Selector, ExcludeSelector: body.ExcludeSelector, ProfileID: body.ProfileID, GatewayUserID: body.GatewayUserID, UsageType: body.UsageType, Currency: body.Currency, SettlementMode: body.SettlementMode, Discount: body.Discount, FxRate: body.FxRate}
	err = s.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&row).Error; err != nil {
			return err
		}
		return bindSource(tx, row, keys)
	})
	if errors.Is(err, errMappingOverlap) && body.Kind != "report" {
		s.fail(w, 409, err.Error())
		return
	}
	if err != nil {
		s.fail(w, 409, "Source channels are already assigned or could not be saved")
		return
	}
	s.write(w, 201, row)
}

func (s *Server) updateSource(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		s.fail(w, 400, "Invalid source configuration")
		return
	}
	var body Source
	if !s.decode(w, r, &body) {
		return
	}
	keys, err := s.validateSource(&body)
	if err != nil {
		s.fail(w, 400, err.Error())
		return
	}
	var row Source
	if err := s.DB.First(&row, id).Error; err != nil {
		s.fail(w, 404, "Source not found")
		return
	}
	// Bindings are permanent: moving a source would hide previously billed costs.
	// A mapping (selector) can change; it only steers later syncs.
	if body.SupplierID != row.SupplierID || body.BaseURL != row.BaseURL || body.ChannelIDs != row.ChannelIDs || body.GatewayUserID != row.GatewayUserID || body.Currency != row.Currency || body.Kind != row.Kind || body.ProfileID != row.ProfileID {
		s.fail(w, 400, "Source bindings cannot be changed")
		return
	}
	supplier, ok := s.targetSupplier(w, r, row.SupplierID)
	if !ok {
		return
	}
	if body.SettlementMode == "settlement" {
		fx, _ := decimal.NewFromString(body.FxRate)
		if supplier.Currency == body.Currency && !fx.Equal(decimal.NewFromInt(1)) {
			s.fail(w, 400, "Same-currency exchange rate must be 1")
			return
		}
	}
	err = s.DB.Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&Source{}).Where("id = ? AND version = ?", row.ID, row.Version).Updates(map[string]any{"name": body.Name, "usage_type": body.UsageType, "credential_env": body.CredentialEnv, "settlement_mode": body.SettlementMode, "discount": body.Discount, "fx_rate": body.FxRate, "selector": body.Selector, "exclude_selector": body.ExcludeSelector, "version": row.Version + 1})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errConflict
		}
		if row.Kind == "report" {
			return nil
		}
		body.ID = row.ID
		return bindSource(tx, body, keys)
	})
	if errors.Is(err, errConflict) {
		s.fail(w, 409, "Record changed; retry the request")
		return
	}
	if errors.Is(err, errMappingOverlap) {
		s.fail(w, 409, err.Error())
		return
	}
	if err != nil {
		s.fail(w, 500, "Database operation failed")
		return
	}
	s.write(w, 200, map[string]bool{"ok": true})
}

func (s *Server) syncSource(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		s.fail(w, 400, "Invalid source configuration")
		return
	}
	var body struct {
		Start string `json:"start"`
		End   string `json:"end"`
	}
	if !s.decode(w, r, &body) {
		return
	}
	start, end, err := statementRange(body.Start, body.End)
	if err != nil || end.Sub(start) > 90*24*time.Hour || !validDate(body.End) {
		s.fail(w, 400, "Sync range must be within 91 days and not in the future")
		return
	}
	var source Source
	if err := s.DB.First(&source, id).Error; err != nil {
		s.fail(w, 404, "Source not found")
		return
	}
	if _, err := s.validateSource(&source); err != nil {
		s.fail(w, 400, err.Error())
		return
	}
	fetched, through, err := FetchSourceUsage(r.Context(), source, start, end)
	rows := make([]ImportedUsage, 0, len(fetched))
	for _, imported := range fetched {
		if source.mapsValue(imported.MatchValue) {
			rows = append(rows, imported)
		}
	}
	if err != nil {
		// Store only the adapter's sanitized errors, never a URL, response, or token.
		s.DB.Model(&Source{}).Where("id = ? AND version = ?", source.ID, source.Version).Update("last_error", err.Error())
		s.fail(w, 502, err.Error())
		return
	}
	created, adjusted := 0, 0
	now := time.Now().Unix()
	actor := currentUser(r).ID
	err = s.DB.Transaction(func(tx *gorm.DB) error {
		// An optimistic write serializes source imports without dialect-specific locks.
		result := tx.Model(&Source{}).Where("id = ? AND version = ?", source.ID, source.Version).Updates(map[string]any{"version": source.Version + 1, "last_sync_at": now, "last_error": ""})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errConflict
		}
		// Resolve channel ownership once for the whole import. Supplier remains
		// the financial head; vendor metadata cannot redirect its charges.
		channelSet := make(map[int64]struct{})
		channelIDs := make([]int64, 0)
		for _, imported := range rows {
			if _, exists := channelSet[imported.ChannelID]; !exists {
				channelSet[imported.ChannelID] = struct{}{}
				channelIDs = append(channelIDs, imported.ChannelID)
			}
		}
		vendorByChannel := make(map[int64]int64)
		if len(channelIDs) > 0 {
			vendorKeys := []VendorKey{}
			if err := tx.Where("base_url = ? AND channel_id IN ?", source.BaseURL, channelIDs).Find(&vendorKeys).Error; err != nil {
				return err
			}
			vendorSet := make(map[int64]struct{})
			vendorIDs := make([]int64, 0, len(vendorKeys))
			for _, key := range vendorKeys {
				// Text collation may be case-insensitive in MySQL. URL path
				// ownership still requires an exact canonical URL match.
				if key.BaseURL != source.BaseURL {
					continue
				}
				if key.VendorID <= 0 || key.BindingKey != digest([]any{source.BaseURL, key.ChannelID}) {
					return errVendorMapping
				}
				if _, exists := vendorSet[key.VendorID]; !exists {
					vendorSet[key.VendorID] = struct{}{}
					vendorIDs = append(vendorIDs, key.VendorID)
				}
			}
			if len(vendorIDs) > 0 {
				vendors := []Vendor{}
				if err := tx.Where("id IN ?", vendorIDs).Find(&vendors).Error; err != nil {
					return err
				}
				vendorsByID := make(map[int64]Vendor, len(vendors))
				for _, vendor := range vendors {
					vendorsByID[vendor.ID] = vendor
				}
				for _, key := range vendorKeys {
					if key.BaseURL != source.BaseURL {
						continue
					}
					vendor, exists := vendorsByID[key.VendorID]
					if !exists || vendor.BaseURL != source.BaseURL {
						return errVendorMapping
					}
					if vendor.SupplierID != source.SupplierID {
						return errVendorOwnership
					}
					vendorByChannel[key.ChannelID] = vendor.ID
				}
			}
		}
		seen := make(map[string]bool, len(rows))
		for _, imported := range rows {
			key := digest([]any{"sync", source.ID, imported.ExternalRef})
			seen[key] = true
			var existing Usage
			lookup := tx.Where("idempotency_key = ?", key).First(&existing).Error
			if lookup != nil && !errors.Is(lookup, gorm.ErrRecordNotFound) {
				return lookup
			}
			if lookup == nil {
				updates := map[string]any{"channel_id": imported.ChannelID, "quantity": imported.Quantity, "request_count": imported.RequestCount, "input_tokens": imported.InputTokens, "output_tokens": imported.OutputTokens, "cache_read_tokens": imported.CacheReadTokens, "cache_write_tokens": imported.CacheWriteTokens}
				if vendorID := vendorByChannel[imported.ChannelID]; vendorID != 0 {
					updates["vendor_id"] = vendorID
				}
				if existing.SettlementStatus == "unpriced" && source.SettlementMode == "usage_only" {
					if err := tx.Model(&existing).Updates(updates).Error; err != nil {
						return err
					}
					continue
				}
				unitPrice, discount, fxRate := existing.UnitPrice, existing.Discount, existing.FxRate
				kind, note := "usage_adjustment", "Source quantity correction"
				if existing.SettlementStatus == "unpriced" {
					// A first agreed price finalizes previously unpriced usage once.
					// Later syncs continue to use this immutable pricing snapshot.
					unitPrice, discount, fxRate = "1", source.Discount, source.FxRate
					updates["unit_price"], updates["discount"], updates["fx_rate"] = unitPrice, discount, fxRate
					updates["settlement_status"] = "settled"
					kind, note = "usage", ""
				}
				rateMode := existing.RateMode
				if existing.SettlementStatus == "unpriced" {
					rateMode = "multiply"
					updates["rate_mode"] = rateMode
				}
				amount, err := calculateCharge(imported.Quantity, unitPrice, discount, fxRate, rateMode)
				if err != nil {
					return err
				}
				oldAmount, err := decimal.NewFromString(existing.Amount)
				if err != nil {
					return err
				}
				newAmount, _ := decimal.NewFromString(amount)
				delta := oldAmount.Sub(newAmount)
				updates["amount"] = amount
				if err := tx.Model(&existing).Updates(updates).Error; err != nil {
					return err
				}
				if !delta.IsZero() {
					if err := tx.Create(&LedgerEntry{SupplierID: source.SupplierID, Date: existing.Date, Kind: kind, Amount: delta.StringFixed(6), UsageID: existing.ID, Note: note, CreatedBy: actor, CreatedAt: now}).Error; err != nil {
						return err
					}
					adjusted++
				}
				continue
			}
			amount, unitPrice, status := "0.000000", "", "unpriced"
			if source.SettlementMode == "settlement" {
				var err error
				amount, err = calculateCharge(imported.Quantity, "1", source.Discount, source.FxRate, "multiply")
				if err != nil {
					return err
				}
				unitPrice, status = "1", "settled"
			}
			row := Usage{SupplierID: source.SupplierID, Date: imported.Date, SourceID: source.ID, ChannelID: imported.ChannelID, VendorID: vendorByChannel[imported.ChannelID], SourceName: source.Name, ExternalRef: imported.ExternalRef, MatchValue: imported.MatchValue, UsageType: source.UsageType, Description: imported.Description, Quantity: imported.Quantity, Unit: imported.Unit, UnitPrice: unitPrice, Currency: source.Currency, Discount: source.Discount, FxRate: source.FxRate, RateMode: "multiply", Amount: amount, SettlementStatus: status, Origin: "sync", RequestCount: imported.RequestCount, InputTokens: imported.InputTokens, OutputTokens: imported.OutputTokens, CacheReadTokens: imported.CacheReadTokens, CacheWriteTokens: imported.CacheWriteTokens, CreatedBy: actor, CreatedAt: now, IdempotencyKey: key, PayloadHash: digest(imported)}
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
			if status == "settled" {
				a, _ := decimal.NewFromString(amount)
				if err := tx.Create(&LedgerEntry{SupplierID: source.SupplierID, Date: row.Date, Kind: "usage", Amount: a.Neg().StringFixed(6), UsageID: row.ID, CreatedBy: actor, CreatedAt: now}).Error; err != nil {
					return err
				}
			}
			created++
		}
		// A vanished row can indicate an incomplete source response. Fail closed;
		// do not turn missing upstream data into a refund or silently retain totals.
		// Rows whose value was unmapped since import stay as billed history.
		previous := []Usage{}
		if err := tx.Where("source_id = ? AND date >= ? AND date <= ?", source.ID, body.Start, min(body.End, through)).Find(&previous).Error; err != nil {
			return err
		}
		for _, row := range previous {
			if !seen[row.IdempotencyKey] && source.mapsValue(row.MatchValue) {
				return errors.New("Previously billed source rows are missing; sync was not applied")
			}
		}
		// Days after the upstream's complete date stay unsynced, not zero.
		for day := start; !day.After(end) && day.Format("2006-01-02") <= through; day = day.AddDate(0, 0, 1) {
			coverage := SourceSync{SourceID: source.ID, Date: day.Format("2006-01-02"), SyncedAt: now}
			if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "source_id"}, {Name: "date"}}, DoUpdates: clause.AssignmentColumns([]string{"synced_at"})}).Create(&coverage).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		message := "Source sync could not be saved"
		if errors.Is(err, errConflict) {
			message = "Record changed; retry the request"
		}
		if strings.HasPrefix(err.Error(), "Previously billed") {
			message = err.Error()
		}
		if errors.Is(err, errVendorOwnership) || errors.Is(err, errVendorMapping) {
			message = err.Error()
		}
		s.DB.Model(&Source{}).Where("id = ? AND version = ?", source.ID, source.Version).Update("last_error", message)
		s.fail(w, 409, message)
		return
	}
	s.write(w, 200, map[string]any{"ok": true, "created": created, "adjusted": adjusted, "records": len(rows)})
}
