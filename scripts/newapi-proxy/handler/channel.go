package handler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"newapi-proxy/config"
	"newapi-proxy/middleware"
	"newapi-proxy/store"
)

// ChannelList returns only the channels owned by the calling user.
// Superadmin (role >= 100) sees all channels.
func ChannelList(w http.ResponseWriter, r *http.Request) {
	uid := middleware.UserIDFromCtx(r)
	role := middleware.RoleFromCtx(r)

	body, status, err := fetchUpstreamBody(r, "/api/channel/")
	if err != nil {
		jsonErr(w, http.StatusBadGateway, err.Error())
		return
	}
	if status != http.StatusOK {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		w.Write(body)
		return
	}

	// Superadmin sees everything.
	if role >= 100 {
		w.Header().Set("Content-Type", "application/json")
		w.Write(body)
		return
	}

	var wrapper struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
		Data    struct {
			Channels []json.RawMessage `json:"channels"`
			Total    int               `json:"total"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &wrapper); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.Write(body)
		return
	}

	owned, err := ownerChannelIDs(uid)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "ownership lookup failed")
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
	w.Header().Set("Content-Type", "application/json")
	w.Write(out)
}

// ChannelCreate creates a channel on upstream and records ownership.
func ChannelCreate(w http.ResponseWriter, r *http.Request) {
	uid := middleware.UserIDFromCtx(r)

	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		jsonErr(w, http.StatusBadRequest, "cannot read request body")
		return
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost,
		config.RemoteURL+"/api/channel/", bytes.NewReader(bodyBytes))
	if err != nil {
		jsonErr(w, http.StatusBadGateway, err.Error())
		return
	}
	req.Header.Set("Authorization", "Bearer "+config.RemoteAdminToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := getUpstreamClient().Do(req)
	if err != nil {
		jsonErr(w, http.StatusBadGateway, err.Error())
		return
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode == http.StatusOK {
		var result struct {
			Data struct {
				ID int64 `json:"id"`
			} `json:"data"`
		}
		if json.Unmarshal(respBody, &result) == nil && result.Data.ID > 0 {
			if err := recordOwnership(uid, result.Data.ID); err != nil {
				fmt.Printf("[channel] recordOwnership uid=%d cid=%d: %v\n", uid, result.Data.ID, err)
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	w.Write(respBody)
}

// ChannelDispatch handles GET/PUT/DELETE /api/channel/:id.
// idStr is the raw path segment after /api/channel/.
func ChannelDispatch(w http.ResponseWriter, r *http.Request, idStr string) {
	// Strip trailing slash if any
	idStr = strings.TrimSuffix(idStr, "/")
	// If there is a sub-path (e.g. /api/channel/42/test), pass through directly
	if strings.Contains(idStr, "/") {
		doUpstream(w, r, r.Method, r.URL.Path, r.Body)
		return
	}

	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid channel id")
		return
	}

	uid := middleware.UserIDFromCtx(r)
	role := middleware.RoleFromCtx(r)

	if role < 100 {
		owns, err := assertOwns(uid, id)
		if err != nil {
			jsonErr(w, http.StatusInternalServerError, "ownership check failed")
			return
		}
		if !owns {
			jsonErr(w, http.StatusForbidden, "forbidden")
			return
		}
	}

	switch r.Method {
	case http.MethodGet:
		doUpstream(w, r, http.MethodGet, fmt.Sprintf("/api/channel/%d", id), nil)
	case http.MethodPut:
		doUpstream(w, r, http.MethodPut, fmt.Sprintf("/api/channel/%d", id), r.Body)
	case http.MethodDelete:
		if role < 100 {
			store.DB.Exec(`DELETE FROM proxy_channel_ownership WHERE remote_channel_id=$1 AND user_id=$2`, id, uid)
		}
		doUpstream(w, r, http.MethodDelete, fmt.Sprintf("/api/channel/%d", id), nil)
	default:
		doUpstream(w, r, r.Method, r.URL.Path, r.Body)
	}
}

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
		var cid int64
		if err := rows.Scan(&cid); err != nil {
			return nil, err
		}
		out[cid] = true
	}
	return out, nil
}

func recordOwnership(userID, channelID int64) error {
	_, err := store.DB.Exec(
		`INSERT INTO proxy_channel_ownership (remote_channel_id, user_id, created_at)
		 VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`,
		channelID, userID, time.Now().Unix(),
	)
	return err
}

func assertOwns(userID, channelID int64) (bool, error) {
	var n int
	err := store.DB.QueryRow(
		`SELECT COUNT(*) FROM proxy_channel_ownership WHERE remote_channel_id=$1 AND user_id=$2`,
		channelID, userID,
	).Scan(&n)
	return n > 0, err
}
