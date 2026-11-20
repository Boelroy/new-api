package main

// 腾讯一站 · 统一新建用户. One form creates the rs_auth_user login of any of
// Tencent's three platforms; the platform role decides which existing gate
// lets it in:
//
//   - supplier_01 (role 4): /api/supplier-account (账号上号, requireRoleOrSupplier)
//   - supplier_ccmax (8) / ccmax_manager (9): /api/ccmax (requireRoleOrCCMaxRoles)
//   - proxy (role 1): scripts/newapi-proxy logs in against rs_auth_user and
//     only checks status + the bcrypt hash, never the role, so proxy users get
//     the plain user tier, which opens nothing else beyond studio-scoped views.
//
// KHub (pd-maas, supplier_02 role 6) is not a Tencent platform: its users are
// created on its own pages and are not counted as third_party here.
//
// studio is required: it is the statement's 供应商名称 (column A). As with CC
// Max members the server generates the password, stores only its bcrypt hash
// and returns it once; it never reaches logs or list responses.

import (
	"log"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
)

// tencentUserRoles maps the form's platform role to the rs_auth_user tier.
var tencentUserRoles = map[string]int{
	"supplier_01":    minSupplierRole,
	"supplier_ccmax": minSupplierCCMaxRole,
	"ccmax_manager":  minCCMaxManagerRole,
	"proxy":          minUserRole,
}

// tencentPlatformRoles lists the tiers whose users belong to a platform; a
// user owning proxy channels belongs to proxy whatever their tier.
var tencentPlatformRoles = map[string][]int{
	tencentPlatformThirdParty: {minSupplierRole},
	tencentPlatformCCMax:      {minSupplierCCMaxRole, minCCMaxManagerRole},
	tencentPlatformProxy:      {minUserRole},
}

const tencentStudioMaxLen = 64

func handleTencentUserCreate(c *gin.Context) {
	var body struct {
		Username  string `json:"username"`
		Studio    string `json:"studio"`
		Role      string `json:"role"`
		ManagerID int64  `json:"manager_id"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	username := strings.TrimSpace(body.Username)
	studio := strings.TrimSpace(body.Studio)
	role, ok := tencentUserRoles[body.Role]
	switch {
	case !ok:
		c.JSON(http.StatusBadRequest, gin.H{"error": "role 须为 supplier_01 / supplier_ccmax / ccmax_manager / proxy"})
		return
	case !ccmaxValidUsername(username):
		c.JSON(http.StatusBadRequest, gin.H{"error": "用户名须为 3-32 位字母、数字、_ - ."})
		return
	case studio == "" || len([]rune(studio)) > tencentStudioMaxLen:
		c.JSON(http.StatusBadRequest, gin.H{"error": "供应商名称（studio）必填，最多 64 个字符"})
		return
	case body.ManagerID != 0 && role != minSupplierCCMaxRole:
		c.JSON(http.StatusBadRequest, gin.H{"error": "只有 CC Max 号商可以指定归属经理"})
		return
	}
	if body.ManagerID != 0 {
		if m, err := authUserByID(body.ManagerID); err != nil || m.Role != minCCMaxManagerRole || m.Status == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "CC Max manager not found"})
			return
		}
	}

	password, err := ccmaxGeneratePassword()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "password generation failed"})
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "hash error"})
		return
	}
	tx, err := db.Begin()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer tx.Rollback()
	now := time.Now().Unix()
	var id int64
	if err := tx.QueryRow(
		`INSERT INTO rs_auth_user (username, password_hash, role, studio, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $5) RETURNING id`,
		username, string(hash), role, studio, now,
	).Scan(&id); err != nil {
		if strings.Contains(err.Error(), "duplicate") || strings.Contains(err.Error(), "unique") {
			c.JSON(http.StatusConflict, gin.H{"error": "用户名已存在"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	// Bind the matching V2 built-in role now instead of waiting for the next
	// startup's legacy mapping, so the V2 user pages agree at once.
	if _, err := tx.Exec(
		`INSERT INTO rs_user_role (user_id, role_id) SELECT $1, id FROM rs_role WHERE name = $2 ON CONFLICT DO NOTHING`,
		id, builtinRoleNameForLegacy(role),
	); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if body.ManagerID != 0 {
		if _, err := tx.Exec(
			`INSERT INTO rs_ccmax_manager_member (manager_id, member_id, created_at) VALUES ($1, $2, $3)`,
			body.ManagerID, id, now,
		); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}
	if err := tx.Commit(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	log.Printf("[tencent] user %d created %s user %d (%s), studio %q, manager %d",
		tencentCallerID(c), body.Role, id, username, studio, body.ManagerID)

	// Users sharing a studio share column A on the statement and the
	// studio-scoped views; name them so the admin can tell it is intended.
	shared := []string{}
	rows, err := db.Query(`SELECT username FROM rs_auth_user WHERE studio = $1 AND id <> $2 ORDER BY username`, studio, id)
	if err == nil {
		for rows.Next() {
			var name string
			if rows.Scan(&name) == nil {
				shared = append(shared, name)
			}
		}
		rows.Close()
	}
	c.JSON(http.StatusOK, gin.H{
		"id":                 id,
		"username":           username,
		"studio":             studio,
		"role":               body.Role,
		"manager_id":         body.ManagerID,
		"password":           password,
		"studio_shared_with": shared,
	})
}

type tencentUserItem struct {
	ID               int64    `json:"id"`
	Username         string   `json:"username"`
	Studio           string   `json:"studio"`
	Role             int      `json:"role"`
	RoleName         string   `json:"role_name"`
	Platforms        []string `json:"platforms"`
	Status           int      `json:"status"`
	CreatedAt        int64    `json:"created_at"`
	SupplierAccounts int64    `json:"supplier_accounts"`
	CCMaxAccounts    int64    `json:"ccmax_accounts"`
	ProxyChannels    int64    `json:"proxy_channels"`
	ManagerID        int64    `json:"manager_id"`
	ManagerUsername  string   `json:"manager_username"`
	Members          int64    `json:"members"`
}

// handleTencentUserList lists users with their linked data on each platform,
// optionally only those of one platform (?platform=third_party|ccmax|proxy).
func handleTencentUserList(c *gin.Context) {
	platform := c.Query("platform")
	if platform != "" && !slices.Contains(tencentPlatforms, platform) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unknown platform"})
		return
	}
	// proxy_channel_ownership belongs to newapi-proxy and only exists once it
	// has run against this database.
	var hasOwnership bool
	if err := db.QueryRow(`SELECT to_regclass('proxy_channel_ownership') IS NOT NULL`).Scan(&hasOwnership); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	proxyCount := `0::bigint`
	if hasOwnership {
		proxyCount = `(SELECT COUNT(*) FROM proxy_channel_ownership o WHERE o.user_id = u.id)`
	}
	rows, err := db.Query(`
		SELECT u.id, u.username, u.studio, u.role, u.status, u.created_at,
		       (SELECT COUNT(*) FROM rs_supplier_account a WHERE a.uploaded_by = u.id),
		       (SELECT COUNT(*) FROM rs_ccmax_account a WHERE a.uploaded_by = u.id),
		       ` + proxyCount + `,
		       COALESCE(mm.manager_id, 0), COALESCE(m.username, ''),
		       (SELECT COUNT(*) FROM rs_ccmax_manager_member x WHERE x.manager_id = u.id)
		  FROM rs_auth_user u
		  LEFT JOIN rs_ccmax_manager_member mm ON mm.member_id = u.id
		  LEFT JOIN rs_auth_user m ON m.id = mm.manager_id
		 ORDER BY u.id`)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()
	roleNames := map[int]string{}
	for name, role := range tencentUserRoles {
		roleNames[role] = name
	}
	list := []tencentUserItem{}
	for rows.Next() {
		var u tencentUserItem
		if err := rows.Scan(&u.ID, &u.Username, &u.Studio, &u.Role, &u.Status, &u.CreatedAt,
			&u.SupplierAccounts, &u.CCMaxAccounts, &u.ProxyChannels, &u.ManagerID, &u.ManagerUsername, &u.Members); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		u.RoleName = roleNames[u.Role]
		u.Platforms = []string{}
		for _, p := range tencentPlatforms {
			linked := (p == tencentPlatformThirdParty && u.SupplierAccounts > 0) ||
				(p == tencentPlatformCCMax && u.CCMaxAccounts > 0) ||
				(p == tencentPlatformProxy && u.ProxyChannels > 0)
			if linked || slices.Contains(tencentPlatformRoles[p], u.Role) {
				u.Platforms = append(u.Platforms, p)
			}
		}
		if platform != "" && !slices.Contains(u.Platforms, platform) {
			continue
		}
		list = append(list, u)
	}
	if err := rows.Err(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"list": list})
}
