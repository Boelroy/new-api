package main

// Single serialization point for rs_key_pool rows. Anything that returns a
// key row over the wire — GET /keys/pool, GET /keys/active, CSV export,
// usage detail — MUST go through serializeKeyRow. This is the ONLY code
// path that decides whether to include plaintext.
//
// KEY VISIBILITY INVARIANT (V2_PRODUCT_SPEC.md §3.6):
//   - key_masked (last 8 chars with `…` prefix) is ALWAYS present.
//   - key (plaintext) is present ONLY when the row is dead AND the caller
//     holds keys.reveal_dead. "dead" = the linked remote_channel_current
//     row has status = 3 (auto-disabled by upstream).
//   - key_encrypted, key_hash are NEVER serialized.

import "log"

// KeyPoolRow is the internal row shape returned by queries. It carries the
// encrypted blob so serializeKeyRow can decrypt it when a plaintext reveal
// is authorized. The keyEncrypted field is lowercase (unexported) — it
// literally cannot leak through JSON marshaling.
type KeyPoolRow struct {
	ID                int64
	Studio            string
	UploadedBy        int64
	KeyType           string
	KeyLast8          string
	QuotaUSD          *float64
	Models            string
	NamePrefix        string
	GroupName         string
	Status            string
	AssignedProfileID int64
	RemoteChannelID   int64
	FailedReason      string
	CreatedAt         int64
	UpdatedAt         int64

	// RemoteStatus is remote_channel_current.status when the row is linked
	// to a channel; 0 otherwise. status=3 means auto-disabled = dead.
	RemoteStatus int

	// The following fields are populated by drilldown / usage queries that
	// join into remote_channel_current + remote_newapi_profile. Zero
	// values in queries that don't need them.
	ChannelName  string
	ProfileName  string
	UsedQuotaRaw int64

	// keyEncrypted is the AES-GCM ciphertext. Unexported so it never
	// appears in JSON marshaling by mistake. Populated by queryKeyPoolRows.
	keyEncrypted string
}

// KeyPoolDTO is the wire shape. It is the ONLY struct any V2 handler may
// return when the response includes key material.
//
// The `Key` field is `omitempty` — when serializeKeyRow decides plaintext
// should not appear, we set Key = "" and it drops from JSON. The frontend
// contract (V2_PRODUCT_SPEC.md §3.6.2) is: if the `key` field is missing
// or empty, treat as no plaintext available.
type KeyPoolDTO struct {
	ID                int64    `json:"id"`
	Studio            string   `json:"studio"`
	UploadedBy        int64    `json:"uploaded_by"`
	KeyType           string   `json:"key_type"`
	KeyMasked         string   `json:"key_masked"`
	Key               string   `json:"key,omitempty"`
	QuotaUSD          *float64 `json:"quota_usd"`
	Models            string   `json:"models"`
	NamePrefix        string   `json:"name_prefix"`
	GroupName         string   `json:"group"`
	Status            string   `json:"status"`
	AssignedProfileID int64    `json:"assigned_profile_id"`
	RemoteChannelID   int64    `json:"remote_channel_id"`
	RemoteStatus      int      `json:"remote_status"`
	IsDead            bool     `json:"is_dead"`
	FailedReason      string   `json:"failed_reason"`
	CreatedAt         int64    `json:"created_at"`
	UpdatedAt         int64    `json:"updated_at"`

	// Populated in drilldown / usage responses. omitempty on the numeric
	// ones keeps the shape clean for endpoints that don't need them.
	ChannelName  string  `json:"channel_name,omitempty"`
	ProfileName  string  `json:"profile_name,omitempty"`
	UsedQuotaRaw int64   `json:"used_quota_raw,omitempty"`
	UsedUSD      float64 `json:"used_usd,omitempty"`
}

// isDeadRow returns true when the row's linked remote channel is
// auto-disabled by the upstream monitor (remote_channel_current.status=3).
// pool-only rows (never assigned) and pending rows are alive by definition.
func isDeadRow(r KeyPoolRow) bool {
	return r.RemoteStatus == 3
}

// buildMask returns the "…LAST8" mask string for display. Falls back to
// empty string if we somehow don't have 8 chars.
func buildMask(last8 string) string {
	if len(last8) < 4 {
		return ""
	}
	return "…" + last8
}

// serializeKeyRow is the single authorized path from a KeyPoolRow to a
// KeyPoolDTO. It:
//   - always fills KeyMasked from the stored key_last8
//   - only fills Key (plaintext) when the row is dead AND canRevealDead
//     is true (caller holds keys.reveal_dead)
//   - decrypts key_encrypted only when Key will actually be filled,
//     minimising exposure
func serializeKeyRow(r KeyPoolRow, canRevealDead bool) KeyPoolDTO {
	dead := isDeadRow(r)
	dto := KeyPoolDTO{
		ID:                r.ID,
		Studio:            r.Studio,
		UploadedBy:        r.UploadedBy,
		KeyType:           r.KeyType,
		KeyMasked:         buildMask(r.KeyLast8),
		QuotaUSD:          r.QuotaUSD,
		Models:            r.Models,
		NamePrefix:        r.NamePrefix,
		GroupName:         r.GroupName,
		Status:            r.Status,
		AssignedProfileID: r.AssignedProfileID,
		RemoteChannelID:   r.RemoteChannelID,
		RemoteStatus:      r.RemoteStatus,
		IsDead:            dead,
		FailedReason:      r.FailedReason,
		CreatedAt:         r.CreatedAt,
		UpdatedAt:         r.UpdatedAt,
		ChannelName:       r.ChannelName,
		ProfileName:       r.ProfileName,
		UsedQuotaRaw:      r.UsedQuotaRaw,
	}
	if r.UsedQuotaRaw > 0 {
		// quotaPerUnit lives in main.go (500000 raw units = 1 USD).
		dto.UsedUSD = roundTo(float64(r.UsedQuotaRaw)/quotaPerUnit, 6)
	}
	if !dead || !canRevealDead {
		return dto
	}
	// Only path where plaintext is exposed. Decrypt in place.
	plain, err := decryptRemoteToken(r.keyEncrypted)
	if err != nil {
		log.Printf("[v2 keys] plaintext decrypt failed for pool_id=%d: %v", r.ID, sanitizeUpstreamMessage(err.Error()))
		return dto
	}
	dto.Key = plain
	return dto
}
