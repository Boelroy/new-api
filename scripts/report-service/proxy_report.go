package main

// newapi-proxy usage report settings. The proxy (scripts/newapi-proxy,
// handler/report.go) does the pushing — only it holds the upstream token — and
// reads these report_config keys from the shared database every 15s.
// 立即推送 stores a request timestamp the proxy answers by writing the outcome
// to proxy_report_last.

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	cfgProxyReportWebhook = "proxy_report_webhook"
	cfgProxyReportHours   = "proxy_report_hours"
	cfgProxyReportPushAt  = "proxy_report_push_at"
	cfgProxyReportLast    = "proxy_report_last"
)

func handleProxyReportSettingsGet(c *gin.Context) {
	var last any
	if raw := supplierConfigGet(cfgProxyReportLast); raw != "" {
		_ = json.Unmarshal([]byte(raw), &last)
	}
	pushAt, _ := strconv.ParseInt(supplierConfigGet(cfgProxyReportPushAt), 10, 64)
	c.JSON(http.StatusOK, gin.H{
		"webhook": supplierConfigGet(cfgProxyReportWebhook),
		"hours":   supplierConfigGet(cfgProxyReportHours),
		"push_at": pushAt,
		"last":    last,
	})
}

// handleProxyReportSettingsSet saves the webhook (empty = off) and the UTC+8
// push hours (empty = manual only). Omitted fields stay unchanged.
func handleProxyReportSettingsSet(c *gin.Context) {
	var body struct {
		Webhook *string `json:"webhook"`
		Hours   *string `json:"hours"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	if body.Webhook != nil {
		webhook := strings.TrimSpace(*body.Webhook)
		if webhook != "" && !strings.HasPrefix(webhook, "https://") {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Webhook 须以 https:// 开头"})
			return
		}
		if err := supplierConfigSet(cfgProxyReportWebhook, webhook); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}
	if body.Hours != nil {
		hours, err := ccmaxParseReportHours(*body.Hours)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		parts := make([]string, len(hours))
		for i, h := range hours {
			parts[i] = strconv.Itoa(h)
		}
		if err := supplierConfigSet(cfgProxyReportHours, strings.Join(parts, ",")); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}
	handleProxyReportSettingsGet(c)
}

// handleProxyReportPush queues a push for the proxy to send on its next tick.
func handleProxyReportPush(c *gin.Context) {
	if strings.TrimSpace(supplierConfigGet(cfgProxyReportWebhook)) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请先配置 Lark Webhook"})
		return
	}
	pushAt := time.Now().Unix()
	if err := supplierConfigSet(cfgProxyReportPushAt, strconv.FormatInt(pushAt, 10)); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"push_at": pushAt})
}
