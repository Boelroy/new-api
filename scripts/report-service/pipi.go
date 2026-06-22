package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

// Pipi sync pulls daily usage + per-key pricing from System 2's report-service
// (which exposes the same /api/report and /api/allkeys/data endpoints) and
// rolls it into report_pipi_daily for use in profit calculations.

var (
	pipiReportURL    string
	pipiReportAPIKey string
)

// pipiLogRow mirrors LogRow returned by /api/report on System 2.
type pipiLogRow struct {
	Hour        string  `json:"hour"`
	ChannelID   int     `json:"channel_id"`
	ChannelName string  `json:"channel_name"`
	RequestCount int    `json:"request_count"`
	TotalCost   float64 `json:"total_cost"`
}

// pipiChannelRow mirrors ChannelRow returned by /api/allkeys/data on System 2.
type pipiChannelRow struct {
	ID           int      `json:"id"`
	Name         string   `json:"name"`
	Tag          string   `json:"tag"`
	UnitPriceCNY *float64 `json:"unit_price_cny"`
}

func pipiGet(path string, out any) error {
	url := strings.TrimRight(pipiReportURL, "/") + path
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-API-Key", pipiReportAPIKey)
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("pipi %s: status %d body=%s", path, resp.StatusCode, string(body))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// syncPipiOnce pulls [start, end] from System 2 and upserts report_pipi_daily.
func syncPipiOnce(start, end string) error {
	if pipiReportURL == "" || pipiReportAPIKey == "" {
		return fmt.Errorf("pipi sync not configured")
	}

	// 1. Daily usage per channel
	var logs []pipiLogRow
	if err := pipiGet(fmt.Sprintf("/api/report?start=%s&end=%s", start, end), &logs); err != nil {
		return fmt.Errorf("fetch report: %w", err)
	}

	// 2. Per-channel tag + unit_price_cny
	var chans []pipiChannelRow
	if err := pipiGet("/api/allkeys/data", &chans); err != nil {
		return fmt.Errorf("fetch channels: %w", err)
	}
	tagByID := map[int]string{}
	priceByID := map[int]*float64{}
	nameByID := map[int]string{}
	for _, ch := range chans {
		tagByID[ch.ID] = ch.Tag
		nameByID[ch.ID] = ch.Name
		priceByID[ch.ID] = ch.UnitPriceCNY
	}

	// 3. Aggregate logs by (date, channel_id)
	type aggKey struct {
		date string
		chID int
	}
	type aggVal struct {
		channelName  string
		requestCount int
		totalCostUSD float64
	}
	agg := map[aggKey]*aggVal{}
	for _, l := range logs {
		if len(l.Hour) < 10 {
			continue
		}
		k := aggKey{date: l.Hour[:10], chID: l.ChannelID}
		v, ok := agg[k]
		if !ok {
			v = &aggVal{channelName: l.ChannelName}
			agg[k] = v
		}
		if v.channelName == "" {
			v.channelName = l.ChannelName
		}
		v.requestCount += l.RequestCount
		v.totalCostUSD += l.TotalCost
	}

	// 4. UPSERT. Wrap in tx for atomicity on the [start, end] range.
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Delete the date range we're about to write so removed channels don't linger.
	if _, err := tx.Exec(`DELETE FROM report_pipi_daily WHERE date BETWEEN $1 AND $2`, start, end); err != nil {
		return fmt.Errorf("clear range: %w", err)
	}

	stmt, err := tx.Prepare(`
		INSERT INTO report_pipi_daily
		  (date, channel_id, channel_name, channel_tag, request_count, total_cost_usd, unit_price_cny, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	now := time.Now().Unix()
	for k, v := range agg {
		name := v.channelName
		if name == "" {
			name = nameByID[k.chID]
		}
		tag := tagByID[k.chID]
		var unitPrice any
		if p := priceByID[k.chID]; p != nil {
			unitPrice = *p
		}
		if _, err := stmt.Exec(k.date, k.chID, name, tag, v.requestCount, roundTo(v.totalCostUSD, 6), unitPrice, now); err != nil {
			return fmt.Errorf("insert (%s,%d): %w", k.date, k.chID, err)
		}
	}
	return tx.Commit()
}

func startPipiSync() {
	if pipiReportURL == "" || pipiReportAPIKey == "" {
		log.Println("pipi sync disabled (PIPI_REPORT_URL or PIPI_REPORT_API_KEY not set)")
		return
	}
	go func() {
		// Initial run after a short delay to let the service settle.
		time.Sleep(10 * time.Second)
		runPipiSync()
		ticker := time.NewTicker(time.Hour)
		for range ticker.C {
			runPipiSync()
		}
	}()
}

func runPipiSync() {
	// Cover today + the prior 7 days so backfills and late writes from System 2 catch up.
	end := time.Now().UTC().Format("2006-01-02")
	start := time.Now().UTC().AddDate(0, 0, -7).Format("2006-01-02")
	if err := syncPipiOnce(start, end); err != nil {
		log.Printf("pipi sync error (%s..%s): %v", start, end, err)
		return
	}
	log.Printf("pipi sync ok (%s..%s)", start, end)
}
