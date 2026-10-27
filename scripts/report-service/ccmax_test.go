package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Suppliers paste whatever the callback page shows; every shape must yield
// the bare code plus any embedded state.
func TestCCMaxParseCode(t *testing.T) {
	cases := []struct {
		name, raw, code, state string
	}{
		{"bare code", "  abc123  ", "abc123", ""},
		{"code#state", "abc123#st1", "abc123", "st1"},
		{"callback query", "http://localhost:54545/callback?code=abc123&state=st1", "abc123", "st1"},
		{"callback fragment", "http://localhost:54545/callback#code=abc123&state=st1", "abc123", "st1"},
		{"query string", "?code=abc123&state=st1", "abc123", "st1"},
		{"code param carrying #state", "https://x/cb?code=abc123%23st2", "abc123", "st2"},
		{"empty", "", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, state := ccmaxParseCode(tc.raw)
			assert.Equal(t, tc.code, code)
			assert.Equal(t, tc.state, state)
		})
	}
}

// Suppliers either paste a proxy URL or fill the fields; both must yield the
// same canonical URL, and anything that is not a usable socks5 is rejected.
func TestCCMaxProxyURL(t *testing.T) {
	cases := []struct {
		name string
		in   ccmaxProxyInput
		want string
		ok   bool
	}{
		{"fields", ccmaxProxyInput{Host: "1.2.3.4", Port: "1080", Username: "u", Password: "p@ss"}, "socks5://u:p%40ss@1.2.3.4:1080", true},
		{"fields no auth", ccmaxProxyInput{Host: "1.2.3.4", Port: "1080"}, "socks5://1.2.3.4:1080", true},
		{"url", ccmaxProxyInput{URL: " socks5://u:p@1.2.3.4:1080 "}, "socks5://u:p@1.2.3.4:1080", true},
		{"url keeps socks5h", ccmaxProxyInput{URL: "socks5h://u:p@proxy.example.com:1080"}, "socks5h://u:p@proxy.example.com:1080", true},
		{"url without scheme", ccmaxProxyInput{URL: "u:p@1.2.3.4:1080"}, "socks5://u:p@1.2.3.4:1080", true},
		{"url wins over fields", ccmaxProxyInput{URL: "socks5://1.2.3.4:1080", Host: "5.6.7.8", Port: "9"}, "socks5://1.2.3.4:1080", true},
		{"http rejected", ccmaxProxyInput{URL: "http://1.2.3.4:8080"}, "", false},
		{"missing port", ccmaxProxyInput{URL: "socks5://1.2.3.4"}, "", false},
		{"bad port", ccmaxProxyInput{Host: "1.2.3.4", Port: "70000"}, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.in.proxyURL()
			if !tc.ok {
				assert.Error(t, err)
				return
			}
			assert.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// A real claude roster row from the portal (secrets removed): status is a
// numeric flag and the form fields live inside the auth_config string. A type
// mismatch here once dropped every row from the list.
func TestParseCCMaxPortalRow(t *testing.T) {
	raw := []byte(`{"a1_id":10784,"a1_status":"using","account_alias":"claude-长河理想-20261023001","account_type":2,` +
		`"auth_config":"{\"account\":\"leonhuwe@gmail.com\",\"password\":\"x\",\"plan\":\"claude/cc-max-20x\",\"plan_end_time\":\"2026-11-22\",\"plan_start_time\":\"2026-10-23\",\"register_location\":\"US\",\"register_time\":\"2026-10-19\"}",` +
		`"created_at":"2026-10-23 12:02","ext_status":"using",` +
		`"extra":"{\"cpa_usage\": {\"cost\": 303731485, \"utilization\": {\"week_reset_at\": 1793113200, \"week_used_percent\": 12, \"five_hour_reset_at\": 1792746000, \"five_hour_used_percent\": 40.5}, \"input_tokens\": 1200, \"output_tokens\": 300}}",` +
		`"has_cpa_auth_json":true,"id":261817,"is_locked":0,` +
		`"model":"claude/cc-max-20x","models":[{"name":"claude/cc-max-20x","usable":true}],"project_id":"leonhuwe@gmail.com",` +
		`"provider":"claude","status":1,"supplier_id":221,"supplier_name":"长河理想"}`)
	row, err := parseCCMaxPortalRow(raw)
	require.NoError(t, err)
	assert.Equal(t, int64(261817), row.ID)
	assert.Equal(t, "leonhuwe@gmail.com", row.Account)
	assert.Equal(t, "using", row.Status)
	assert.Equal(t, "claude/cc-max-20x", row.Plan)
	assert.Equal(t, "US", row.RegisterLocation)
	assert.Equal(t, "2026-11-22", row.PlanEndTime)
	assert.Equal(t, "claude-20261023001", row.Alias)
	assert.Equal(t, int64(1200), row.InputTokens)
	assert.Equal(t, int64(300), row.OutputTokens)
	assert.InDelta(t, 303.731485, row.CostRMB, 1e-9)
	assert.Equal(t, 40.5, row.FiveHourUsedPercent)
	assert.Equal(t, 12.0, row.WeekUsedPercent)
	assert.Equal(t, int64(1792746000), row.FiveHourResetAt)
	assert.Equal(t, int64(1793113200), row.WeekResetAt)
}

// The portal rejects auth_json unless every field but id_token is set, the
// timestamps are RFC3339 and proxy_url is http(s)/socks5(h); check() must
// catch the same cases before upload.
func TestCCMaxCredentialCheck(t *testing.T) {
	good := ccmaxCredential{
		AccessToken: "a", RefreshToken: "r", LastRefresh: "2026-10-26T10:00:00-04:00",
		Email: "u@example.com", AccountUUID: "acc", OrganizationUUID: "org", OrganizationName: "o",
		ClaudeDeviceIDs: []string{"d"}, Type: "claude", Expired: "2026-10-26T18:00:00-04:00",
		ProxyURL: "socks5://u:p@1.2.3.4:1080", VerificationStatus: "verified",
	}
	require.NoError(t, good.check())

	noOrg := good
	noOrg.OrganizationName, noOrg.OrganizationUUID = "", ""
	assert.ErrorContains(t, noOrg.check(), "organization_name、organization_uuid")

	noDevice := good
	noDevice.ClaudeDeviceIDs = nil
	assert.ErrorContains(t, noDevice.check(), "claude_device_ids")

	badTime := good
	badTime.Expired = "2026-10-26 18:00:00"
	assert.ErrorContains(t, badTime.check(), "RFC3339")

	badProxy := good
	badProxy.ProxyURL = "ftp://1.2.3.4:21"
	assert.ErrorContains(t, badProxy.check(), "proxy_url")
}

// The uploaded 账号 must be the Claude login the credential belongs to;
// case and surrounding spaces don't matter.
func TestCCMaxCredentialMatchesAccount(t *testing.T) {
	cred := ccmaxCredential{Email: "Diva00727@gmail.com"}
	assert.NoError(t, cred.matchesAccount("diva00727@gmail.com"))
	assert.NoError(t, cred.matchesAccount(" DIVA00727@GMAIL.COM "))
	relay := ccmaxCredential{Email: "n74wgpzf6s@privaterelay.appleid.com"}
	assert.Error(t, relay.matchesAccount("diva00727@gmail.com"))
	assert.Error(t, cred.matchesAccount(""))
}

// Month-to-date cost: current cumulative minus the last pre-month snapshot;
// accounts new this month count in full, accounts gone from the roster count
// through their last in-month snapshot, and a counter reset never subtracts.
func TestCCMaxMonthCostMicro(t *testing.T) {
	current := map[int64]int64{1: 500, 2: 300, 4: 100}
	inMonth := map[int64]int64{1: 450, 3: 80}
	baseline := map[int64]int64{1: 200, 3: 50, 4: 900}
	// 1: 500-200, 2: 300 (new), 3: 80-50 (dropped off roster), 4: reset → 0
	assert.Equal(t, int64(300+300+30), ccmaxMonthCostMicro(current, inMonth, baseline))
}

func TestCCMaxParseReportHours(t *testing.T) {
	hours, err := ccmaxParseReportHours(" 22, 10,,10 ")
	require.NoError(t, err)
	assert.Equal(t, []int{10, 22}, hours)
	_, err = ccmaxParseReportHours("24")
	assert.Error(t, err)
	_, err = ccmaxParseReportHours("ten")
	assert.Error(t, err)
}

// The push names the exact month range in UTC+8.
func TestCCMaxReportText(t *testing.T) {
	to := time.Date(2026, 10, 27, 15, 0, 0, 0, supplierPortalLoc)
	text := ccmaxReportText(ccmaxReport{
		Total: 85, Serving: 40, FiveHourAvg: 0.352, WeekAvg: 0.61,
		MonthCostRMB: 720, FxRate: 7.2,
		From: time.Date(2026, 10, 1, 0, 0, 0, 0, supplierPortalLoc), To: to,
	})
	assert.Contains(t, text, "账号总数：85")
	assert.Contains(t, text, "正在跑：40")
	assert.Contains(t, text, "5 小时窗口平均：35.2%")
	assert.Contains(t, text, "7 天窗口平均：61.0%")
	assert.Contains(t, text, "本月消耗：$100.00（¥720.00")
	assert.Contains(t, text, "统计区间：2026-10-01 00:00 ~ 2026-10-27 15:00（UTC+8）")
}
