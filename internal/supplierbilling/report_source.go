package supplierbilling

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/shopspring/decimal"
)

// ImportedUsage contains source facts only. The caller snapshots the supplier's
// discount and exchange rate when posting the corresponding ledger entry.
type ImportedUsage struct {
	ExternalRef      string
	ChannelID        int64
	MatchValue       string
	Date             string
	Description      string
	Quantity         string
	Unit             string
	RequestCount     int64
	InputTokens      int64
	OutputTokens     int64
	CacheReadTokens  int64
	CacheWriteTokens int64
}

var reportSourceCredentialEnv = regexp.MustCompile(`^SUPPLIER_BILLING_SOURCE_[A-Z0-9_]+$`)

// FetchReportUsage reads hourly Report Service aggregates and assigns them to
// Beijing business dates. total_cost is gateway USD usage, not an official
// provider price; it must not be relabeled as undiscounted provider spend.
func FetchReportUsage(ctx context.Context, source Source, start, end time.Time) ([]ImportedUsage, error) {
	if source.GatewayUserID < 0 || source.GatewayUserID > maxGatewayUserID {
		return nil, errors.New("Invalid source configuration")
	}
	channelList := source.Kind != "report_group" && source.Kind != "report_studio"
	channels := make(map[int64]struct{})
	if channelList {
		for raw := range strings.SplitSeq(source.ChannelIDs, ",") {
			id, parseErr := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
			if parseErr != nil || id <= 0 {
				return nil, errors.New("Report source requires positive channel IDs")
			}
			channels[id] = struct{}{}
		}
	}
	location := businessZone
	startLocal, endLocal := start.In(location), end.In(location)
	from := time.Date(startLocal.Year(), startLocal.Month(), startLocal.Day(), 0, 0, 0, 0, location)
	until := time.Date(endLocal.Year(), endLocal.Month(), endLocal.Day(), 0, 0, 0, 0, location).AddDate(0, 0, 1)
	if start.IsZero() || end.IsZero() || !from.Before(until) {
		return nil, errors.New("Invalid report date range")
	}
	query := url.Values{}
	query.Set("start", from.UTC().Format("2006-01-02"))
	query.Set("end", until.UTC().Format("2006-01-02"))
	body, err := fetchSourceJSON(ctx, source, "/api/report", query)
	if err != nil {
		return nil, err
	}
	if len(body) == 0 || body[0] != '[' {
		return nil, errors.New("Report source must return an array")
	}
	var rows []struct {
		Hour             string       `json:"hour"`
		Group            string       `json:"group"`
		UserID           *int64       `json:"user_id"`
		TokenID          *int64       `json:"token_id"`
		ChannelID        *int64       `json:"channel_id"`
		Model            string       `json:"model"`
		Studio           string       `json:"studio"`
		RequestCount     *int64       `json:"request_count"`
		InputTokens      *int64       `json:"input_tokens"`
		OutputTokens     *int64       `json:"output_tokens"`
		CacheReadTokens  *int64       `json:"cache_read_tokens"`
		CacheWriteTokens *int64       `json:"cache_write_tokens"`
		TotalCost        *json.Number `json:"total_cost"`
	}
	if err := common.Unmarshal(body, &rows); err != nil {
		return nil, errors.New("Report source returned invalid usage data")
	}
	const maxQuantity = "1000000000"
	maxAmount := decimal.RequireFromString(maxQuantity)
	const maxCount = int64(9007199254740991)
	seen := make(map[string]struct{}, len(rows))
	result := make([]ImportedUsage, 0, len(rows))
	for _, row := range rows {
		hour, parseErr := time.ParseInLocation("2006-01-02 15:04", row.Hour, time.UTC)
		if parseErr != nil || hour.Format("2006-01-02 15:04") != row.Hour || hour.Minute() != 0 || strings.TrimSpace(row.Model) == "" || len(row.Model) > 1024 {
			return nil, errors.New("Report source returned an invalid usage period or model")
		}
		if row.UserID == nil || *row.UserID < 0 || row.TokenID == nil || *row.TokenID < 0 || row.ChannelID == nil || *row.ChannelID <= 0 || row.TotalCost == nil {
			return nil, errors.New("Report source returned incomplete usage data")
		}
		for _, value := range []*int64{row.RequestCount, row.InputTokens, row.OutputTokens, row.CacheReadTokens, row.CacheWriteTokens} {
			if value == nil || *value < 0 || *value > maxCount {
				return nil, errors.New("Report source returned an invalid usage count")
			}
		}
		// Bound the text before decimal parsing: an exponent such as 1e999999
		// must never trigger an unbounded allocation during comparison/rounding.
		amountText := row.TotalCost.String()
		if len(amountText) == 0 || len(amountText) > 32 || strings.ContainsAny(amountText, "eE+-") {
			return nil, errors.New("Report source returned an invalid usage amount")
		}
		amount, parseErr := decimal.NewFromString(amountText)
		if parseErr != nil || amount.IsNegative() || amount.Exponent() < -6 || amount.GreaterThan(maxAmount) {
			return nil, errors.New("Report source returned an invalid usage amount")
		}
		identity, marshalErr := common.Marshal([]any{row.Hour, *row.UserID, *row.TokenID, *row.ChannelID, row.Model})
		if marshalErr != nil {
			return nil, errors.New("Report source usage identity is invalid")
		}
		digest := sha256.Sum256(identity)
		externalRef := hex.EncodeToString(digest[:])
		if _, duplicate := seen[externalRef]; duplicate {
			return nil, errors.New("Report source returned duplicate usage data")
		}
		seen[externalRef] = struct{}{}
		if _, allowed := channels[*row.ChannelID]; (channelList && !allowed) || hour.Before(from) || !hour.Before(until) {
			continue
		}
		if source.GatewayUserID != 0 && *row.UserID != source.GatewayUserID {
			continue
		}
		matchValue := row.Group
		if source.Kind == "report_studio" {
			// An upstream without the studio field cannot attribute usage; fail
			// closed rather than bill it to an "all other studios" source.
			matchValue = strings.TrimSpace(row.Studio)
			if matchValue == "" || len(matchValue) > 120 {
				return nil, errors.New("Report source returned incomplete usage data")
			}
		}
		result = append(result, ImportedUsage{
			ExternalRef:      externalRef,
			ChannelID:        *row.ChannelID,
			MatchValue:       matchValue,
			Date:             hour.In(location).Format("2006-01-02"),
			Description:      row.Model,
			Quantity:         amount.String(),
			Unit:             "gateway_usd",
			RequestCount:     *row.RequestCount,
			InputTokens:      *row.InputTokens,
			OutputTokens:     *row.OutputTokens,
			CacheReadTokens:  *row.CacheReadTokens,
			CacheWriteTokens: *row.CacheWriteTokens,
		})
	}
	if source.Kind != "report_group" {
		return result, nil
	}
	// Group sources keep one row per day, group and model instead of per hour,
	// token and channel; channels are recreated daily and are not the mapping.
	grouped := make([]ImportedUsage, 0)
	index := map[string]int{}
	for _, row := range result {
		identity := digest([]any{"group", row.Date, row.MatchValue, row.Description})
		position, exists := index[identity]
		if !exists {
			position = len(grouped)
			index[identity] = position
			grouped = append(grouped, ImportedUsage{ExternalRef: identity, Date: row.Date, MatchValue: row.MatchValue, Description: row.Description, Quantity: "0", Unit: row.Unit})
		}
		current := &grouped[position]
		quantity := decimal.RequireFromString(current.Quantity).Add(decimal.RequireFromString(row.Quantity))
		current.Quantity = quantity.String()
		current.RequestCount += row.RequestCount
		current.InputTokens += row.InputTokens
		current.OutputTokens += row.OutputTokens
		current.CacheReadTokens += row.CacheReadTokens
		current.CacheWriteTokens += row.CacheWriteTokens
	}
	return grouped, nil
}
