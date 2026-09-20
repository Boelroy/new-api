package handler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"newapi-proxy/config"
	"newapi-proxy/store"
)

// channelListResponse is a partial decode of the upstream channel list so we
// can filter to only the rows the caller owns.
type channelListResponse struct {
	Success bool            `json:"success"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

type channelRow struct {
	ID   int64           `json:"id"`
	Rest json.RawMessage `json:"-"`
}

// fetchUpstream GETs path from upstream and returns the decoded JSON or an error.
func fetchUpstream(ctx *gin.Context, path string) ([]byte, int, error) {
	url := config.RemoteURL + path
	if q := ctx.Request.URL.RawQuery; q != "" {
		url += "?" + q
	}
	req, err := http.NewRequestWithContext(ctx.Request.Context(), http.MethodGet, url, nil)
	if err != nil {
		return nil, http.StatusBadGateway, err
	}
	req.Header.Set("Authorization", "Bearer "+config.RemoteAdminToken)
	resp, err := getUpstreamClient().Do(req)
	if err != nil {
		return nil, http.StatusBadGateway, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	return body, resp.StatusCode, err
}

// ownerChannelIDs returns the set of remote channel IDs owned by userID.
func ownerChannelIDs(userID int64) (map[int64]bool, error) {
	rows, err := store.DB.Query(
		`SELECT remote_channel_id FROM proxy_channel_ownership WHERE user_id=$1`, userID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[int64]bool)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, nil
}

// recordOwnership inserts a proxy_channel_ownership row, ignoring conflicts.
func recordOwnership(userID, channelID int64) error {
	_, err := store.DB.Exec(
		`INSERT INTO proxy_channel_ownership (remote_channel_id, user_id, created_at)
		 VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`,
		channelID, userID, time.Now().Unix(),
	)
	return err
}

// assertOwns returns true when userID owns channelID.
func assertOwns(userID, channelID int64) (bool, error) {
	var n int
	err := store.DB.QueryRow(
		`SELECT COUNT(*) FROM proxy_channel_ownership WHERE remote_channel_id=$1 AND user_id=$2`,
		channelID, userID,
	).Scan(&n)
	return n > 0, err
}

// ChannelList returns only the channels owned by the calling user.
// Superadmin (role >= 100) sees all channels.
func ChannelList(c *gin.Context) {
	userID, _ := c.Get("user_id")
	uid, _ := userID.(int64)
	role, _ := c.Get("role")
	r, _ := role.(int)

	body, status, err := fetchUpstream(c, "/api/channel/")
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"success": false, "message": err.Error()})
		return
	}
	if status != http.StatusOK {
		c.Data(status, "application/json", body)
		return
	}

	// Superadmin: pass through as-is.
	if r >= 100 {
		c.Data(http.StatusOK, "application/json", body)
		return
	}

	// Decode the upstream response to filter rows.
	var wrapper struct {
		Success bool `json:"success"`
		Message string `json:"message"`
		Data    struct {
			Channels []json.RawMessage `json:"channels"`
			Total    int               `json:"total"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &wrapper); err != nil {
		// Unexpected shape — pass through.
		c.Data(http.StatusOK, "application/json", body)
		return
	}

	owned, err := ownerChannelIDs(uid)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "ownership lookup failed"})
		return
	}

	filtered := make([]json.RawMessage, 0)
	for _, raw := range wrapper.Data.Channels {
		var ch struct {
			ID int64 `json:"id"`
		}
		if json.Unmarshal(raw, &ch) == nil && owned[ch.ID] {
			filtered = append(filtered, raw)
		}
	}
	wrapper.Data.Channels = filtered
	wrapper.Data.Total = len(filtered)

	out, _ := json.Marshal(wrapper)
	c.Data(http.StatusOK, "application/json", out)
}

// ChannelCreate creates a channel on upstream and records ownership.
func ChannelCreate(c *gin.Context) {
	userID, _ := c.Get("user_id")
	uid, _ := userID.(int64)

	bodyBytes, err := readBody(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "cannot read request body"})
		return
	}

	req, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost,
		config.RemoteURL+"/api/channel/", bytes.NewReader(bodyBytes))
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"success": false, "message": err.Error()})
		return
	}
	req.Header.Set("Authorization", "Bearer "+config.RemoteAdminToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"success": false, "message": err.Error()})
		return
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode == http.StatusOK {
		// Extract the new channel id and record ownership.
		var result struct {
			Data struct {
				ID int64 `json:"id"`
			} `json:"data"`
		}
		if json.Unmarshal(respBody, &result) == nil && result.Data.ID > 0 {
			if err := recordOwnership(uid, result.Data.ID); err != nil {
				// Non-fatal: log but still return success to the client.
				fmt.Printf("[channel] recordOwnership uid=%d cid=%d: %v\n", uid, result.Data.ID, err)
			}
		}
	}

	c.Data(resp.StatusCode, "application/json", respBody)
}

// channelIDFromPath extracts the numeric id from paths like /api/channel/42 or /api/channel/42/...
func channelIDFromPath(c *gin.Context) (int64, bool) {
	idStr := c.Param("id")
	if idStr == "" {
		return 0, false
	}
	id, err := strconv.ParseInt(idStr, 10, 64)
	return id, err == nil
}

// ChannelGet returns a single channel only if the caller owns it (or is superadmin).
func ChannelGet(c *gin.Context) {
	id, ok := channelIDFromPath(c)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid channel id"})
		return
	}
	userID, _ := c.Get("user_id")
	uid, _ := userID.(int64)
	role, _ := c.Get("role")
	r, _ := role.(int)

	if r < 100 {
		owns, err := assertOwns(uid, id)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "ownership check failed"})
			return
		}
		if !owns {
			c.JSON(http.StatusForbidden, gin.H{"success": false, "message": "forbidden"})
			return
		}
	}

	do(c, http.MethodGet, fmt.Sprintf("/api/channel/%d", id), nil)
}

// ChannelUpdate updates a channel only if the caller owns it (or is superadmin).
func ChannelUpdate(c *gin.Context) {
	id, ok := channelIDFromPath(c)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid channel id"})
		return
	}
	userID, _ := c.Get("user_id")
	uid, _ := userID.(int64)
	role, _ := c.Get("role")
	r, _ := role.(int)

	if r < 100 {
		owns, err := assertOwns(uid, id)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "ownership check failed"})
			return
		}
		if !owns {
			c.JSON(http.StatusForbidden, gin.H{"success": false, "message": "forbidden"})
			return
		}
	}

	do(c, http.MethodPut, fmt.Sprintf("/api/channel/%d", id), c.Request.Body)
}

// ChannelDelete deletes a channel only if the caller owns it (or is superadmin).
func ChannelDelete(c *gin.Context) {
	id, ok := channelIDFromPath(c)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid channel id"})
		return
	}
	userID, _ := c.Get("user_id")
	uid, _ := userID.(int64)
	role, _ := c.Get("role")
	r, _ := role.(int)

	if r < 100 {
		owns, err := assertOwns(uid, id)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "ownership check failed"})
			return
		}
		if !owns {
			c.JSON(http.StatusForbidden, gin.H{"success": false, "message": "forbidden"})
			return
		}
		// Remove ownership row on delete.
		store.DB.Exec(`DELETE FROM proxy_channel_ownership WHERE remote_channel_id=$1 AND user_id=$2`, id, uid)
	}

	do(c, http.MethodDelete, fmt.Sprintf("/api/channel/%d", id), nil)
}
