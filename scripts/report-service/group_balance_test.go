package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func usd(v float64) *float64 { return &v }

// An exhausted key must contribute 0 remaining rather than a negative number,
// otherwise it cancels out a healthy key's balance and suppresses the alert.
func TestAggregateGroupBalancesClampsExhaustedKeys(t *testing.T) {
	balances := aggregateGroupBalances([]ChannelRow{
		{Group: "vip", QuotaUSD: usd(100), UsedUSD: 180, LastHourUSD: 2}, // over quota by $80
		{Group: "vip", QuotaUSD: usd(100), UsedUSD: 90, LastHourUSD: 3},  // $10 left
		{Group: "free", QuotaUSD: nil, UsedUSD: 5, LastHourUSD: 1},       // unmetered
	})
	require.Len(t, balances, 2)

	free, vip := balances[0], balances[1]
	require.Equal(t, "free", free.Group)
	require.Equal(t, "vip", vip.Group)

	assert.Equal(t, 2, vip.ChannelsWithQuota)
	assert.InDelta(t, 200, vip.TotalQuotaUSD, 1e-9)
	assert.InDelta(t, 270, vip.TotalUsedUSD, 1e-9)
	assert.InDelta(t, 10, vip.TotalRemainingUSD, 1e-9)
	require.NotNil(t, vip.ETAHours)
	assert.InDelta(t, 2, *vip.ETAHours, 1e-9)

	assert.Equal(t, 0, free.ChannelsWithQuota)
	assert.Zero(t, free.TotalRemainingUSD)
}

// A channel serving several groups counts in each of them, and remaining never
// drops below zero even when every key in the group is exhausted.
func TestAggregateGroupBalancesSharedAndFullyExhausted(t *testing.T) {
	balances := aggregateGroupBalances([]ChannelRow{
		{Group: "a, b", QuotaUSD: usd(50), UsedUSD: 75, LastHourUSD: 4},
		{Group: "", QuotaUSD: usd(20), UsedUSD: 20},
	})
	require.Len(t, balances, 3)

	for _, b := range balances {
		assert.Zerof(t, b.TotalRemainingUSD, "group %s", b.Group)
	}
	assert.Equal(t, "a", balances[0].Group)
	assert.Equal(t, "b", balances[1].Group)
	assert.Equal(t, "default", balances[2].Group)

	require.NotNil(t, balances[0].ETAHours)
	assert.Zero(t, *balances[0].ETAHours)
	assert.Nil(t, balances[2].ETAHours)
}
