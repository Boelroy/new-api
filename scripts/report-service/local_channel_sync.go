package main

// Sync a stored remote-channel credential into a LOCAL new-api channel.
//
// The vertex/aws/pending upload flows persist the upstream key, encrypted,
// in remote_channel_credential (keyed by profile_id, remote_channel_id, with
// channel_type / key_type / region / settings_json). Those are real, still
// valid upstream credentials — but there was no way to stand one up as a
// channel on the LOCAL new-api (the gateway this report-service shares a
// Postgres with).
//
// This does exactly that: decrypt the credential, resolve models/group from
// the owning profile's per-type defaults, and INSERT a channels + abilities
// (+ report_key_quotas) row directly — the same insert sequence
// insertLocalChannelForPending / handleBatchCreateChannels already use. new-api
// picks the new channel up on its next cache refresh.
//
// Admin+ only: the routes are mounted under the requireRole(minAdminRole)
// group in main.go. remote_local_sync records the credential → local channel
// mapping so the operation is idempotent and the UI can show synced state.

import (
	"database/sql"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// syncableCredential is one row of the syncable list: a stored credential
// joined with its profile defaults and (when still present) its live mirror
// row. resolvedModels/resolvedGroup are what a sync would actually write.
type syncableCredential struct {
	ProfileID       int64  `json:"profile_id"`
	ProfileName     string `json:"profile_name"`
	RemoteChannelID int64  `json:"remote_channel_id"`
	ChannelType     int    `json:"channel_type"`
	ChannelTypeName string `json:"channel_type_name"`
	KeyType         string `json:"key_type"`
	Region          string `json:"region"`
	KeyMasked       string `json:"key_masked"`
	ResolvedModels  string `json:"resolved_models"`
	ResolvedGroup   string `json:"resolved_group"`
	// ChannelName is the live mirror name when the remote channel still
	// exists; empty when the remote channel was already deleted.
	ChannelName    string `json:"channel_name"`
	AlreadySynced  bool   `json:"already_synced"`
	LocalChannelID int64  `json:"local_channel_id"`
	CreatedAt      int64  `json:"created_at"`
}

// channelTypeLabel maps the new-api channel type integer to a short label for
// the UI. Only the types this report-service actually stores credentials for
// are named; anything else falls back to "type N".
func channelTypeLabel(t int) string {
	switch t {
	case 1:
		return "OpenAI"
	case 14:
		return "Anthropic"
	case 24:
		return "Gemini"
	case 41:
		return "Vertex AI"
	default:
		return fmt.Sprintf("type %d", t)
	}
}

// profileDefaults carries the per-type default models/group columns off
// remote_newapi_profile so resolveTypeDefaults can pick the right pair.
type profileDefaults struct {
	models       string
	group        string
	geminiGroup  string
	geminiModels string
	vertexModels string
	openaiGroup  string
	openaiModels string
}

// resolveTypeDefaults returns the (models, group) a sync should use for a
// channel of the given type, sourced from the profile's per-type defaults.
// currentModels / currentGroup are the live mirror values when the remote
// channel still exists; they win over the profile defaults so a sync
// reproduces the channel as it was configured upstream.
func resolveTypeDefaults(channelType int, currentModels, currentGroup string, d profileDefaults) (models, group string) {
	switch channelType {
	case 41: // Vertex AI — Vertex model names differ from AI Studio.
		models, group = d.vertexModels, d.group
	case 24: // Gemini (AI Studio)
		models, group = d.geminiModels, d.geminiGroup
	case 1: // OpenAI
		models, group = d.openaiModels, d.openaiGroup
	default: // 14 Anthropic and everything else
		models, group = d.models, d.group
	}
	if strings.TrimSpace(currentModels) != "" {
		models = currentModels
	}
	if strings.TrimSpace(currentGroup) != "" {
		group = currentGroup
	}
	if strings.TrimSpace(group) == "" {
		group = "default"
	}
	return strings.TrimSpace(models), strings.TrimSpace(group)
}

// syncableSelect is the shared projection for both the list and the single
// lookup. The WHERE clause is appended by the caller.
const syncableSelect = `
	SELECT cr.profile_id, cr.remote_channel_id, cr.channel_type, cr.key_type,
	       cr.region, cr.settings_json, cr.key_encrypted, cr.created_at,
	       p.name,
	       p.default_models, p.default_group, p.default_gemini_group,
	       p.default_gemini_models, p.default_vertex_models,
	       p.default_openai_group, p.default_openai_models,
	       COALESCE(cc.name, ''), COALESCE(cc.models, ''), COALESCE(cc."group", ''),
	       COALESCE(ls.channel_id, 0)
	  FROM remote_channel_credential cr
	  JOIN remote_newapi_profile p ON p.id = cr.profile_id
	  LEFT JOIN remote_channel_current cc
	         ON cc.profile_id = cr.profile_id AND cc.remote_channel_id = cr.remote_channel_id
	  LEFT JOIN remote_local_sync ls
	         ON ls.profile_id = cr.profile_id AND ls.remote_channel_id = cr.remote_channel_id`

// credentialRow is everything a sync needs, scanned from syncableSelect.
type credentialRow struct {
	profileID       int64
	profileName     string
	remoteChannelID int64
	channelType     int
	keyType         string
	region          string
	settingsJSON    string
	keyEncrypted    string
	createdAt       int64
	currentName     string
	currentModels   string
	currentGroup    string
	syncedChannelID int64
	defaults        profileDefaults
}

func scanCredentialRow(scan func(dest ...any) error) (credentialRow, error) {
	var r credentialRow
	err := scan(
		&r.profileID, &r.remoteChannelID, &r.channelType, &r.keyType,
		&r.region, &r.settingsJSON, &r.keyEncrypted, &r.createdAt,
		&r.profileName,
		&r.defaults.models, &r.defaults.group, &r.defaults.geminiGroup,
		&r.defaults.geminiModels, &r.defaults.vertexModels,
		&r.defaults.openaiGroup, &r.defaults.openaiModels,
		&r.currentName, &r.currentModels, &r.currentGroup,
		&r.syncedChannelID,
	)
	return r, err
}

// handleLocalSyncList (GET /api/remote-newapi/local-sync/syncable) returns
// every stored credential with its resolved sync target and synced state.
func handleLocalSyncList(c *gin.Context) {
	rows, err := db.Query(syncableSelect + ` ORDER BY cr.profile_id, cr.remote_channel_id`)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()

	items := make([]syncableCredential, 0)
	for rows.Next() {
		r, err := scanCredentialRow(rows.Scan)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		models, group := resolveTypeDefaults(r.channelType, r.currentModels, r.currentGroup, r.defaults)
		masked := ""
		if plain, derr := decryptRemoteToken(r.keyEncrypted); derr == nil {
			masked = "…" + channelKeyTail(plain, 8)
		}
		items = append(items, syncableCredential{
			ProfileID:       r.profileID,
			ProfileName:     r.profileName,
			RemoteChannelID: r.remoteChannelID,
			ChannelType:     r.channelType,
			ChannelTypeName: channelTypeLabel(r.channelType),
			KeyType:         r.keyType,
			Region:          strings.Join(strings.Fields(r.region), " "),
			KeyMasked:       masked,
			ResolvedModels:  models,
			ResolvedGroup:   group,
			ChannelName:     r.currentName,
			AlreadySynced:   r.syncedChannelID > 0,
			LocalChannelID:  r.syncedChannelID,
			CreatedAt:       r.createdAt,
		})
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

// localSyncResult is one item's outcome in the batch sync response.
type localSyncResult struct {
	ProfileID       int64  `json:"profile_id"`
	RemoteChannelID int64  `json:"remote_channel_id"`
	OK              bool   `json:"ok"`
	Skipped         bool   `json:"skipped"`
	ChannelID       int64  `json:"channel_id,omitempty"`
	Error           string `json:"error,omitempty"`
}

// handleLocalSyncCreate (POST /api/remote-newapi/local-sync) syncs a batch of
// credentials into local channels. Each item is independent — one failure
// doesn't abort the rest.
func handleLocalSyncCreate(c *gin.Context) {
	var body struct {
		Items []struct {
			ProfileID       int64 `json:"profile_id"`
			RemoteChannelID int64 `json:"remote_channel_id"`
		} `json:"items"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body: " + err.Error()})
		return
	}
	if len(body.Items) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "items is required"})
		return
	}

	callerID := int64(0)
	if v, ok := c.Get("user_id"); ok {
		if uid, ok := v.(int64); ok {
			callerID = uid
		}
	}

	results := make([]localSyncResult, 0, len(body.Items))
	okCount := 0
	for _, it := range body.Items {
		res := localSyncResult{ProfileID: it.ProfileID, RemoteChannelID: it.RemoteChannelID}
		channelID, skipped, err := syncCredentialToLocalChannel(it.ProfileID, it.RemoteChannelID, callerID)
		if err != nil {
			res.Error = err.Error()
		} else {
			res.OK = true
			res.Skipped = skipped
			res.ChannelID = channelID
			okCount++
		}
		results = append(results, res)
	}
	c.JSON(http.StatusOK, gin.H{"results": results, "ok": okCount, "total": len(body.Items)})
}

// syncCredentialToLocalChannel decrypts one stored credential and creates the
// matching local channel. Returns skipped=true (with the existing channel id)
// when a local channel already carries the same key, so re-syncing is a no-op.
func syncCredentialToLocalChannel(profileID, remoteChannelID, callerID int64) (channelID int64, skipped bool, err error) {
	row := db.QueryRow(syncableSelect+` WHERE cr.profile_id = $1 AND cr.remote_channel_id = $2`,
		profileID, remoteChannelID)
	cred, err := scanCredentialRow(row.Scan)
	if err == sql.ErrNoRows {
		return 0, false, fmt.Errorf("no stored credential for profile %d channel %d", profileID, remoteChannelID)
	}
	if err != nil {
		return 0, false, err
	}

	key, err := decryptRemoteToken(cred.keyEncrypted)
	if err != nil {
		return 0, false, fmt.Errorf("decrypt credential: %v", err)
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return 0, false, fmt.Errorf("stored credential is empty")
	}

	models, group := resolveTypeDefaults(cred.channelType, cred.currentModels, cred.currentGroup, cred.defaults)
	if models == "" {
		return 0, false, fmt.Errorf("no models: set the profile's default models for %s (type %d) first",
			channelTypeLabel(cred.channelType), cred.channelType)
	}
	channelType := cred.channelType
	if channelType == 0 {
		channelType = 14
	}

	// Dedupe: a local channel already carrying this exact key means a prior
	// sync (or a manual add) already landed it. Record the mapping and skip.
	var existingID int64
	switch derr := db.QueryRow(`SELECT id FROM channels WHERE key = $1 LIMIT 1`, key).Scan(&existingID); derr {
	case nil:
		upsertLocalSyncMap(profileID, remoteChannelID, existingID, key, callerID)
		return existingID, true, nil
	case sql.ErrNoRows:
		// fall through to insert
	default:
		return 0, false, derr
	}

	groupList := splitChannelGroups(group)
	channelGroup := strings.Join(groupList, ",")
	modelList := splitCommaList(models)
	if len(modelList) == 0 {
		return 0, false, fmt.Errorf("no valid models after parsing %q", models)
	}

	name := cred.currentName
	if strings.TrimSpace(name) == "" {
		name = fmt.Sprintf("sync-%s-%d-%s", cred.profileName, remoteChannelID, keySha8(key))
	}
	now := time.Now().Unix()

	tx, err := db.Begin()
	if err != nil {
		return 0, false, fmt.Errorf("begin: %v", err)
	}
	defer tx.Rollback()

	if err := tx.QueryRow(`
		INSERT INTO channels
		(type, key, status, name, weight, created_time, base_url, "group", models,
		 model_mapping, status_code_mapping, priority, auto_ban, used_quota,
		 channel_info, tag, other, settings)
		VALUES ($1, $2, 1, $3, 0, $4, '', $5, $6,
		        '', '', 0, 1, 0,
		        $7::json, '', $8, $9)
		RETURNING id`,
		channelType, key, name, now, channelGroup, models,
		channelInfoDefault, cred.region, cred.settingsJSON,
	).Scan(&channelID); err != nil {
		return 0, false, fmt.Errorf("insert channel: %v", err)
	}

	for _, g := range groupList {
		for _, m := range modelList {
			if _, err := tx.Exec(`
				INSERT INTO abilities ("group", model, channel_id, enabled, priority, weight)
				VALUES ($1, $2, $3, true, 0, 0)
				ON CONFLICT DO NOTHING`,
				g, m, channelID,
			); err != nil {
				return 0, false, fmt.Errorf("insert ability: %v", err)
			}
		}
	}

	// Seed the local quota ledger from the remote channel's operator meta,
	// when present, so the All Keys / Key Capacity balance views have a
	// starting quota. Best-effort — absence is not an error.
	var quotaUSD sql.NullFloat64
	var unitPrice sql.NullFloat64
	_ = db.QueryRow(`SELECT quota_usd, unit_price_cny FROM remote_channel_meta WHERE profile_id=$1 AND remote_channel_id=$2`,
		profileID, remoteChannelID).Scan(&quotaUSD, &unitPrice)
	if quotaUSD.Valid {
		if unitPrice.Valid && unitPrice.Float64 > 0 {
			if _, err := tx.Exec(`
				INSERT INTO report_key_quotas (channel_id, quota_usd, unit_price_cny, updated_at)
				VALUES ($1, $2, $3, $4)
				ON CONFLICT (channel_id) DO UPDATE SET quota_usd=$2, unit_price_cny=$3, updated_at=$4`,
				channelID, quotaUSD.Float64, unitPrice.Float64, now,
			); err != nil {
				return 0, false, fmt.Errorf("insert quota: %v", err)
			}
		} else {
			if _, err := tx.Exec(`
				INSERT INTO report_key_quotas (channel_id, quota_usd, updated_at)
				VALUES ($1, $2, $3)
				ON CONFLICT (channel_id) DO UPDATE SET quota_usd=$2, updated_at=$3`,
				channelID, quotaUSD.Float64, now,
			); err != nil {
				return 0, false, fmt.Errorf("insert quota: %v", err)
			}
		}
	}

	if _, err := tx.Exec(`
		INSERT INTO remote_local_sync (profile_id, remote_channel_id, channel_id, key_hash, created_by, created_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (profile_id, remote_channel_id)
		DO UPDATE SET channel_id=$3, key_hash=$4, created_by=$5, created_at=$6`,
		profileID, remoteChannelID, channelID, pendingKeyHash(key), callerID, now,
	); err != nil {
		return 0, false, fmt.Errorf("record sync map: %v", err)
	}

	if err := tx.Commit(); err != nil {
		return 0, false, fmt.Errorf("commit: %v", err)
	}
	return channelID, false, nil
}

// upsertLocalSyncMap records a credential → local channel mapping outside the
// insert path (used when a matching local channel already existed). Best
// effort; a failure here doesn't fail the caller.
func upsertLocalSyncMap(profileID, remoteChannelID, channelID int64, key string, callerID int64) {
	now := time.Now().Unix()
	_, _ = db.Exec(`
		INSERT INTO remote_local_sync (profile_id, remote_channel_id, channel_id, key_hash, created_by, created_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (profile_id, remote_channel_id)
		DO UPDATE SET channel_id=$3, key_hash=$4, created_by=$5, created_at=$6`,
		profileID, remoteChannelID, channelID, pendingKeyHash(key), callerID, now)
}

// splitChannelGroups expands a comma-joined group list into trimmed, non-empty
// values, mirroring insertLocalChannelForPending. Empty → ["default"].
func splitChannelGroups(group string) []string {
	out := make([]string, 0, 2)
	for _, g := range strings.Split(group, ",") {
		if t := strings.TrimSpace(g); t != "" {
			out = append(out, t)
		}
	}
	if len(out) == 0 {
		out = []string{"default"}
	}
	return out
}

// splitCommaList trims a comma-separated list into non-empty entries.
func splitCommaList(s string) []string {
	out := make([]string, 0)
	for _, part := range strings.Split(s, ",") {
		if t := strings.TrimSpace(part); t != "" {
			out = append(out, t)
		}
	}
	return out
}
