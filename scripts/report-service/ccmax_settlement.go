package main

// CC Max 结算: the stored daily bills (rs_ccmax_daily_bill) summed over a date
// range per settlement party, with each party's days marked settled or not.
//
// A party is who gets paid: a ccmax_manager settles for every supplier_ccmax
// user assigned to it, and an uploader with no manager settles alone. Only
// days with a non-zero bill count; a member assigned to several managers is
// settled through the lowest manager id.

import (
	"cmp"
	"net/http"
	"slices"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/lib/pq"
)

const (
	ccmaxPartyManager = "manager"
	ccmaxPartyUser    = "user"
)

// ccmaxBillRow is one account's stored bill on one day.
type ccmaxBillRow struct {
	DS         string
	UploadedBy int64
	AccountID  int64
	Account    string
	Alias      string
	Micro      int64
}

type ccmaxSettlementAccount struct {
	ID      int64   `json:"id"`
	Account string  `json:"account"`
	Alias   string  `json:"alias"`
	CostRMB float64 `json:"cost_rmb"`
}

type ccmaxSettlementMember struct {
	ID       int64                    `json:"id"`
	Username string                   `json:"username"`
	CostRMB  float64                  `json:"cost_rmb"`
	Accounts []ccmaxSettlementAccount `json:"accounts"`
}

type ccmaxSettlementDay struct {
	DS         string  `json:"ds"`
	CostRMB    float64 `json:"cost_rmb"`
	Settled    bool    `json:"settled"`
	SettledRMB float64 `json:"settled_rmb"` // bill when marked; differs from cost_rmb if re-pulled since
	SettledAt  int64   `json:"settled_at"`
}

type ccmaxSettlementParty struct {
	ID           int64                   `json:"id"`
	Username     string                  `json:"username"`
	Kind         string                  `json:"kind"`
	CostRMB      float64                 `json:"cost_rmb"`
	SettledRMB   float64                 `json:"settled_rmb"`
	UnsettledRMB float64                 `json:"unsettled_rmb"`
	Days         []ccmaxSettlementDay    `json:"days"`
	Members      []ccmaxSettlementMember `json:"members"`
}

// ccmaxSettlementMark is a stored settled day of a party.
type ccmaxSettlementMark struct {
	Micro     int64
	SettledAt int64
}

type ccmaxPartyDay struct {
	party int64
	ds    string
}

// ccmaxBuildSettlement groups bills into settlement parties (managerOf: member
// → manager), sums members, accounts and days, and splits each party's total
// into settled / unsettled by its marked days. Days ascend; parties, members
// and accounts are ordered by cost, largest first.
func ccmaxBuildSettlement(bills []ccmaxBillRow, managerOf map[int64]int64, names map[int64]string, marks map[ccmaxPartyDay]ccmaxSettlementMark) []ccmaxSettlementParty {
	type member struct {
		micro    int64
		accounts map[int64]*ccmaxSettlementAccount
	}
	type party struct {
		kind    string
		micro   int64
		days    map[string]int64
		members map[int64]*member
	}
	parties := map[int64]*party{}
	for _, b := range bills {
		if b.Micro == 0 {
			continue
		}
		partyID, kind := b.UploadedBy, ccmaxPartyUser
		if m, ok := managerOf[b.UploadedBy]; ok {
			partyID, kind = m, ccmaxPartyManager
		}
		p := parties[partyID]
		if p == nil {
			p = &party{kind: kind, days: map[string]int64{}, members: map[int64]*member{}}
			parties[partyID] = p
		}
		p.micro += b.Micro
		p.days[b.DS] += b.Micro
		mem := p.members[b.UploadedBy]
		if mem == nil {
			mem = &member{accounts: map[int64]*ccmaxSettlementAccount{}}
			p.members[b.UploadedBy] = mem
		}
		mem.micro += b.Micro
		acc := mem.accounts[b.AccountID]
		if acc == nil {
			acc = &ccmaxSettlementAccount{ID: b.AccountID, Account: b.Account, Alias: b.Alias}
			mem.accounts[b.AccountID] = acc
		}
		acc.CostRMB += float64(b.Micro) / 1e6
	}

	out := make([]ccmaxSettlementParty, 0, len(parties))
	for id, p := range parties {
		sp := ccmaxSettlementParty{ID: id, Username: names[id], Kind: p.kind, CostRMB: float64(p.micro) / 1e6}
		var settled int64
		for ds, micro := range p.days {
			day := ccmaxSettlementDay{DS: ds, CostRMB: float64(micro) / 1e6}
			if mark, ok := marks[ccmaxPartyDay{id, ds}]; ok {
				day.Settled, day.SettledRMB, day.SettledAt = true, float64(mark.Micro)/1e6, mark.SettledAt
				settled += micro
			}
			sp.Days = append(sp.Days, day)
		}
		slices.SortFunc(sp.Days, func(a, b ccmaxSettlementDay) int { return cmp.Compare(a.DS, b.DS) })
		sp.SettledRMB = float64(settled) / 1e6
		sp.UnsettledRMB = float64(p.micro-settled) / 1e6
		for uid, m := range p.members {
			sm := ccmaxSettlementMember{ID: uid, Username: names[uid], CostRMB: float64(m.micro) / 1e6}
			for _, a := range m.accounts {
				sm.Accounts = append(sm.Accounts, *a)
			}
			slices.SortFunc(sm.Accounts, func(a, b ccmaxSettlementAccount) int { return cmp.Compare(b.CostRMB, a.CostRMB) })
			sp.Members = append(sp.Members, sm)
		}
		slices.SortFunc(sp.Members, func(a, b ccmaxSettlementMember) int { return cmp.Compare(b.CostRMB, a.CostRMB) })
		out = append(out, sp)
	}
	slices.SortFunc(out, func(a, b ccmaxSettlementParty) int { return cmp.Compare(b.CostRMB, a.CostRMB) })
	return out
}

// ccmaxManagerOf maps each assigned member to its settling manager (the
// lowest manager id when assigned to several).
func ccmaxManagerOf() (map[int64]int64, error) {
	rows, err := db.Query(`SELECT member_id, MIN(manager_id) FROM rs_ccmax_manager_member GROUP BY member_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]int64{}
	for rows.Next() {
		var member, manager int64
		if err := rows.Scan(&member, &manager); err != nil {
			return nil, err
		}
		out[member] = manager
	}
	return out, rows.Err()
}

// handleCCMaxSettlement returns the settlement parties for [from, to]
// (default: this month), scoped like the account list: admins see every
// party, a ccmax_manager its own, a supplier_ccmax only its own bills (inside
// whichever party settles them).
func handleCCMaxSettlement(c *gin.Context) {
	now := time.Now().In(supplierPortalLoc)
	fromDS := cmp.Or(c.Query("from"), ccmaxMonthStart(now).Format("2006-01-02"))
	toDS := cmp.Or(c.Query("to"), now.Format("2006-01-02"))
	if _, _, err := ccmaxParseDayRange(fromDS, toDS); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	all, uids, err := ccmaxVisibleUploaders(c)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	query := `SELECT to_char(b.ds, 'YYYY-MM-DD'), a.uploaded_by, b.remote_account_id, a.account, a.alias, b.cost_micro
	            FROM rs_ccmax_daily_bill b JOIN rs_ccmax_account a ON a.remote_account_id = b.remote_account_id
	           WHERE b.ds BETWEEN $1 AND $2 AND b.cost_micro <> 0`
	args := []any{fromDS, toDS}
	if !all {
		query += ` AND a.uploaded_by = ANY($3)`
		args = append(args, pq.Array(uids))
	}
	rows, err := db.Query(query, args...)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	bills := []ccmaxBillRow{}
	for rows.Next() {
		var b ccmaxBillRow
		if err := rows.Scan(&b.DS, &b.UploadedBy, &b.AccountID, &b.Account, &b.Alias, &b.Micro); err != nil {
			rows.Close()
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		b.Alias = ccmaxDisplayAlias(b.Alias)
		bills = append(bills, b)
	}
	rows.Close()

	managerOf, err := ccmaxManagerOf()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	names := map[int64]string{}
	rows, err = db.Query(`SELECT id, username FROM rs_auth_user`)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	for rows.Next() {
		var id int64
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			rows.Close()
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		names[id] = name
	}
	rows.Close()

	marks := map[ccmaxPartyDay]ccmaxSettlementMark{}
	rows, err = db.Query(
		`SELECT party_id, to_char(ds, 'YYYY-MM-DD'), amount_micro, settled_at FROM rs_ccmax_settlement WHERE ds BETWEEN $1 AND $2`,
		fromDS, toDS)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()
	for rows.Next() {
		var k ccmaxPartyDay
		var m ccmaxSettlementMark
		if err := rows.Scan(&k.party, &k.ds, &m.Micro, &m.SettledAt); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		marks[k] = m
	}

	c.JSON(http.StatusOK, gin.H{
		"from":           fromDS,
		"to":             toDS,
		"synced_through": supplierConfigGet(cfgCCMaxBillSyncedThrough),
		"bill_hour":      ccmaxBillHour,
		"fx_rate":        supplierFxRate(),
		"parties":        ccmaxBuildSettlement(bills, managerOf, names, marks),
	})
}

// handleCCMaxSettlementMark marks (settled=true) or unmarks a party's days
// (admin). A marked day records the party's bill for that day at marking time.
func handleCCMaxSettlementMark(c *gin.Context) {
	var body struct {
		PartyID int64    `json:"party_id"`
		Days    []string `json:"days"`
		Settled bool     `json:"settled"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.PartyID <= 0 || len(body.Days) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "party_id and days are required"})
		return
	}
	for _, ds := range body.Days {
		if _, err := time.ParseInLocation("2006-01-02", ds, supplierPortalLoc); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "日期格式须为 YYYY-MM-DD"})
			return
		}
	}
	if !body.Settled {
		if _, err := db.Exec(`DELETE FROM rs_ccmax_settlement WHERE party_id=$1 AND ds = ANY($2::date[])`,
			body.PartyID, pq.Array(body.Days)); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
		return
	}

	// The party's bill per day: its own uploads when it settles alone, its
	// members' when it is a manager (members settled through another manager
	// excluded).
	managerOf, err := ccmaxManagerOf()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	uploaders := []int64{}
	for member, manager := range managerOf {
		if manager == body.PartyID {
			uploaders = append(uploaders, member)
		}
	}
	if _, managed := managerOf[body.PartyID]; !managed {
		uploaders = append(uploaders, body.PartyID)
	}
	rows, err := db.Query(
		`SELECT to_char(b.ds, 'YYYY-MM-DD'), SUM(b.cost_micro)
		   FROM rs_ccmax_daily_bill b JOIN rs_ccmax_account a ON a.remote_account_id = b.remote_account_id
		  WHERE a.uploaded_by = ANY($1) AND b.ds = ANY($2::date[]) GROUP BY b.ds`,
		pq.Array(uploaders), pq.Array(body.Days))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	amounts := map[string]int64{}
	for rows.Next() {
		var ds string
		var micro int64
		if err := rows.Scan(&ds, &micro); err != nil {
			rows.Close()
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		amounts[ds] = micro
	}
	rows.Close()

	uidAny, _ := c.Get("user_id")
	by, _ := uidAny.(int64)
	now := time.Now().Unix()
	tx, err := db.Begin()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer tx.Rollback()
	for _, ds := range body.Days {
		if _, err := tx.Exec(
			`INSERT INTO rs_ccmax_settlement (party_id, ds, amount_micro, settled_at, settled_by) VALUES ($1, $2, $3, $4, $5)
			 ON CONFLICT (party_id, ds) DO UPDATE SET amount_micro=EXCLUDED.amount_micro, settled_at=EXCLUDED.settled_at, settled_by=EXCLUDED.settled_by`,
			body.PartyID, ds, amounts[ds], now, by,
		); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}
	if err := tx.Commit(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
