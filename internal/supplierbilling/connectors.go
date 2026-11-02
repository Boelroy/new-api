package supplierbilling

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/shopspring/decimal"
)

// fetchSourceJSON performs one authenticated Report Service GET. Errors are
// fixed sentences: they are stored on the source and shown to suppliers, so
// they must never contain a URL, response body or credential.
func fetchSourceJSON(ctx context.Context, source Source, endpoint string, query url.Values) ([]byte, error) {
	base, err := url.Parse(strings.TrimSpace(source.BaseURL))
	if err != nil || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" || base.Opaque != "" {
		return nil, errors.New("Invalid report source URL")
	}
	if base.Scheme != "https" && !(base.Scheme == "http" && (base.Hostname() == "localhost" || base.Hostname() == "127.0.0.1" || base.Hostname() == "::1")) {
		return nil, errors.New("Report source requires HTTPS")
	}
	if !reportSourceCredentialEnv.MatchString(source.CredentialEnv) {
		return nil, errors.New("Report source credential environment variable is invalid")
	}
	credential, ok := os.LookupEnv(source.CredentialEnv)
	if !ok || strings.TrimSpace(credential) == "" || strings.ContainsAny(credential, "\r\n") {
		return nil, errors.New("Report source credential is not configured")
	}
	base.Path = strings.TrimRight(base.Path, "/") + endpoint
	base.RawPath = ""
	base.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base.String(), nil)
	if err != nil {
		return nil, errors.New("Invalid report source request")
	}
	req.Header.Set("X-API-Key", credential)
	req.Header.Set("Accept", "application/json")
	client := &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	response, err := client.Do(req)
	if err != nil {
		return nil, errors.New("Report source request failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, errors.New("Report source returned an unsuccessful response")
	}
	const maxResponseBytes = 32 << 20
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil || len(body) > maxResponseBytes {
		return nil, errors.New("Report source response is incomplete or too large")
	}
	return bytes.TrimSpace(body), nil
}

// FetchSourceUsage returns every upstream value's daily usage for the range
// and the last Beijing date the upstream has completed. The caller applies the
// source mapping, so discovery and sync read the same rows.
func FetchSourceUsage(ctx context.Context, source Source, start, end time.Time) ([]ImportedUsage, string, error) {
	endText := end.In(businessZone).Format("2006-01-02")
	if source.Kind == "report" || source.Kind == "report_group" {
		rows, err := FetchReportUsage(ctx, source, start, end)
		return rows, endText, err
	}
	query := url.Values{}
	query.Set("start", start.In(businessZone).Format("2006-01-02"))
	query.Set("end", endText)
	endpoint := "/api/billing/ccmax-usage"
	if source.Kind == "remote_tag" {
		endpoint = "/api/billing/remote-tag-usage"
		query.Set("profile_id", strconv.FormatInt(source.ProfileID, 10))
	}
	body, err := fetchSourceJSON(ctx, source, endpoint, query)
	if err != nil {
		return nil, "", err
	}
	var payload struct {
		Through *string `json:"through"`
		Rows    []struct {
			Date     string  `json:"date"`
			Tag      string  `json:"tag"`
			USD      *string `json:"usd"`
			Channels int64   `json:"channels"`
			Username string  `json:"username"`
			Accounts int64   `json:"accounts"`
			CNY      *string `json:"cny"`
		} `json:"rows"`
	}
	if len(body) == 0 || body[0] != '{' || common.Unmarshal(body, &payload) != nil || payload.Through == nil || payload.Rows == nil {
		return nil, "", errors.New("Report source returned invalid usage data")
	}
	through := *payload.Through
	if through != "" && !validDate(through) {
		return nil, "", errors.New("Report source returned an invalid usage period or model")
	}
	startText := start.In(businessZone).Format("2006-01-02")
	seen := map[string]bool{}
	result := make([]ImportedUsage, 0, len(payload.Rows))
	for _, row := range payload.Rows {
		value, amount, unit, description := row.Tag, row.USD, "gateway_usd", row.Tag
		if source.Kind == "ccmax" {
			// List price converted to USD by the Report Service FX rate; the
			// original CNY stays in the description for reconciliation.
			value, amount, unit = row.Username, row.USD, "list_usd"
			description = row.Username + " · " + strconv.FormatInt(row.Accounts, 10) + " accounts"
			if row.CNY != nil {
				description += " · ¥" + *row.CNY
			}
		}
		if !validDate(row.Date) || strings.TrimSpace(value) == "" || len(value) > 120 || amount == nil {
			return nil, "", errors.New("Report source returned incomplete usage data")
		}
		if row.Date < startText || row.Date > endText {
			continue
		}
		quantity, err := billingDecimal(*amount, 1_000_000_000, true)
		if err != nil {
			return nil, "", errors.New("Report source returned an invalid usage amount")
		}
		identity := digest([]any{source.Kind, source.ProfileID, row.Date, value})
		if seen[identity] {
			return nil, "", errors.New("Report source returned duplicate usage data")
		}
		seen[identity] = true
		result = append(result, ImportedUsage{ExternalRef: identity, MatchValue: value, Date: row.Date, Description: description, Quantity: quantity.String(), Unit: unit})
	}
	return result, through, nil
}

// MappingOption is one upstream value seen recently, with the source that
// currently maps it (zero when it is unmapped).
type MappingOption struct {
	Value      string `json:"value"`
	Quantity   string `json:"quantity"`
	Unit       string `json:"unit"`
	SourceID   int64  `json:"source_id"`
	SourceName string `json:"source_name"`
	SupplierID int64  `json:"supplier_id"`
}

// discoverMapping lists the last 14 days of upstream values for the mapping
// editor, so managers pick groups, tags or uploaders instead of typing them.
func (s *Server) discoverMapping(w http.ResponseWriter, r *http.Request) {
	var body Source
	if !s.decode(w, r, &body) {
		return
	}
	if body.Kind == "report" {
		s.fail(w, 400, "Invalid source configuration")
		return
	}
	if err := s.validateSourceEndpoint(&body); err != nil {
		s.fail(w, 400, err.Error())
		return
	}
	if body.Kind == "remote_tag" && body.ProfileID <= 0 {
		s.fail(w, 400, "Select a remote profile")
		return
	}
	end := time.Now().In(businessZone)
	rows, _, err := FetchSourceUsage(r.Context(), body, end.AddDate(0, 0, -13), end)
	if err != nil {
		s.fail(w, 502, err.Error())
		return
	}
	totals := map[string]decimal.Decimal{}
	units := map[string]string{}
	for _, row := range rows {
		totals[row.MatchValue] = totals[row.MatchValue].Add(decimal.RequireFromString(row.Quantity))
		units[row.MatchValue] = row.Unit
	}
	sources := []Source{}
	if err := s.DB.Where("base_url = ? AND kind = ?", body.BaseURL, body.Kind).Find(&sources).Error; err != nil {
		s.fail(w, 500, "Database operation failed")
		return
	}
	// Explicit mappings win over an "all other tags" source.
	slices.SortStableFunc(sources, func(a, b Source) int {
		if a.ExcludeSelector == b.ExcludeSelector {
			return 0
		}
		if a.ExcludeSelector {
			return 1
		}
		return -1
	})
	options := make([]MappingOption, 0, len(totals))
	for value, total := range totals {
		option := MappingOption{Value: value, Quantity: total.StringFixed(6), Unit: units[value]}
		for _, source := range sources {
			if source.BaseURL == body.BaseURL && source.ProfileID == body.ProfileID && source.mapsValue(value) {
				option.SourceID, option.SourceName, option.SupplierID = source.ID, source.Name, source.SupplierID
				break
			}
		}
		options = append(options, option)
	}
	slices.SortFunc(options, func(a, b MappingOption) int {
		return decimal.RequireFromString(b.Quantity).Cmp(decimal.RequireFromString(a.Quantity))
	})
	s.write(w, 200, options)
}
