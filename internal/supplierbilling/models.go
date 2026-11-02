package supplierbilling

type Supplier struct {
	ID        int64  `json:"id" gorm:"primaryKey"`
	Name      string `json:"name" gorm:"size:120;not null"`
	Currency  string `json:"currency" gorm:"size:3;not null"`
	CreatedAt int64  `json:"created_at"`
}

func (Supplier) TableName() string { return "sb_suppliers" }

// Vendor is a supplier studio beneath a settlement head. Supplier keeps the
// existing head identity used by accounts, sources, recharges and the ledger.
type Vendor struct {
	ID           int64  `json:"id" gorm:"primaryKey"`
	SupplierID   int64  `json:"supplier_id" gorm:"index;not null"`
	Name         string `json:"name" gorm:"size:120;not null"`
	SourceStudio string `json:"source_studio" gorm:"size:120;not null"`
	BaseURL      string `json:"-" gorm:"type:text;not null"`
	IdentityKey  string `json:"-" gorm:"size:64;uniqueIndex;not null"`
	CreatedAt    int64  `json:"created_at"`
}

func (Vendor) TableName() string { return "sb_vendors" }

// VendorKey is upstream channel ownership metadata; it never contains an API
// key. BindingKey is the same canonical URL/channel digest as SourceChannel.
type VendorKey struct {
	ID         int64  `json:"-" gorm:"primaryKey"`
	VendorID   int64  `json:"-" gorm:"index;not null"`
	BaseURL    string `json:"-" gorm:"type:text;not null"`
	ChannelID  int64  `json:"-" gorm:"not null"`
	BindingKey string `json:"-" gorm:"size:64;uniqueIndex;not null"`
}

func (VendorKey) TableName() string { return "sb_vendor_keys" }

type User struct {
	ID           int64  `json:"id" gorm:"primaryKey"`
	Username     string `json:"username" gorm:"size:64;uniqueIndex;not null"`
	PasswordHash string `json:"-" gorm:"type:text;not null"`
	Role         string `json:"role" gorm:"size:32;not null"`
	SupplierID   int64  `json:"supplier_id" gorm:"index"`
	Disabled     bool   `json:"disabled"`
	CreatedAt    int64  `json:"created_at"`
}

func (User) TableName() string { return "sb_users" }

const maxGatewayUserID = int64(9007199254740991)

type Source struct {
	ID            int64  `json:"id" gorm:"primaryKey"`
	SupplierID    int64  `json:"supplier_id" gorm:"index;not null"`
	Name          string `json:"name" gorm:"size:120;not null"`
	Kind          string `json:"kind" gorm:"size:32;not null"`
	BaseURL       string `json:"base_url" gorm:"type:text;not null"`
	CredentialEnv string `json:"credential_env" gorm:"size:120;not null"`
	ChannelIDs    string `json:"channel_ids" gorm:"type:text;not null"`
	// Selector maps upstream values to this supplier product line: gateway
	// groups ("report_group"), remote channel tags ("remote_tag") or CC Max
	// uploaders ("ccmax"). ExcludeSelector maps every tag except those listed.
	// Unlike channel bindings, the mapping can be edited; it applies to syncs
	// from then on and never removes usage that was already imported.
	Selector        string `json:"selector" gorm:"type:text"`
	ExcludeSelector bool   `json:"exclude_selector"`
	// ProfileID is the Report Service remote profile read by "remote_tag".
	ProfileID int64 `json:"profile_id" gorm:"not null;default:0"`
	// GatewayUserID identifies an upstream gateway account, never a billing
	// or Report Service user. Zero preserves the existing all-users scope.
	GatewayUserID  int64  `json:"gateway_user_id" gorm:"not null;default:0"`
	UsageType      string `json:"usage_type" gorm:"size:120;not null"`
	Currency       string `json:"currency" gorm:"size:3;not null"`
	SettlementMode string `json:"settlement_mode" gorm:"size:16;not null;default:settlement"`
	Discount       string `json:"discount" gorm:"size:32;not null"`
	FxRate         string `json:"fx_rate" gorm:"size:32;not null"`
	Version        int64  `json:"-"`
	LastSyncAt     int64  `json:"last_sync_at"`
	LastError      string `json:"last_error" gorm:"type:text"`
}

func (Source) TableName() string { return "sb_sources" }

// A source/channel can be billed to only one supplier, including when two
// managers create source bindings concurrently.
type SourceChannel struct {
	ID         int64  `gorm:"primaryKey"`
	SourceID   int64  `gorm:"index;not null"`
	BindingKey string `gorm:"size:64;uniqueIndex;not null"`
}

func (SourceChannel) TableName() string { return "sb_source_channels" }

// Coverage is recorded only after a complete source response commits. No row
// means unknown usage, which must never be presented as a confirmed zero.
type SourceSync struct {
	ID       int64  `gorm:"primaryKey"`
	SourceID int64  `gorm:"uniqueIndex:sb_source_day;not null"`
	Date     string `gorm:"size:10;uniqueIndex:sb_source_day;not null"`
	SyncedAt int64
}

func (SourceSync) TableName() string { return "sb_source_syncs" }

// Usage contains the current quantity and the immutable pricing snapshot.
// Imported quantity corrections append a ledger delta, retaining every charge.
type Usage struct {
	ID          int64  `json:"id" gorm:"primaryKey"`
	SupplierID  int64  `json:"supplier_id" gorm:"index;not null"`
	Date        string `json:"date" gorm:"size:10;index;not null"`
	SourceID    int64  `json:"source_id" gorm:"index"`
	ChannelID   int64  `json:"channel_id" gorm:"not null;default:0"`
	VendorID    int64  `json:"vendor_id" gorm:"index;not null;default:0"`
	SourceName  string `json:"source_name" gorm:"size:120"`
	ExternalRef string `json:"-" gorm:"size:64"`
	// MatchValue is the mapped upstream value (group, tag or uploader).
	MatchValue  string `json:"match_value" gorm:"size:120"`
	UsageType   string `json:"usage_type" gorm:"size:120;not null"`
	Description string `json:"description" gorm:"type:text"`
	Quantity    string `json:"quantity" gorm:"size:32;not null"`
	Unit        string `json:"unit" gorm:"size:64;not null"`
	UnitPrice   string `json:"unit_price" gorm:"size:32;not null"`
	Currency    string `json:"currency" gorm:"size:3;not null"`
	Discount    string `json:"discount" gorm:"size:32;not null"`
	FxRate      string `json:"fx_rate" gorm:"size:32;not null"`
	// RateMode "multiply" quotes settlement units per original unit; "divide"
	// quotes original units per settlement unit, e.g. 6.7 CNY per USD.
	RateMode         string `json:"rate_mode" gorm:"size:16;not null;default:multiply"`
	Amount           string `json:"amount" gorm:"size:32;not null"`
	SettlementStatus string `json:"settlement_status" gorm:"size:16;not null;default:settled"`
	Origin           string `json:"origin" gorm:"size:16;not null"`
	RequestCount     int64  `json:"request_count"`
	InputTokens      int64  `json:"input_tokens"`
	OutputTokens     int64  `json:"output_tokens"`
	CacheReadTokens  int64  `json:"cache_read_tokens"`
	CacheWriteTokens int64  `json:"cache_write_tokens"`
	CreatedBy        int64  `json:"created_by"`
	CreatedAt        int64  `json:"created_at"`
	IdempotencyKey   string `json:"-" gorm:"size:64;uniqueIndex;not null"`
	PayloadHash      string `json:"-" gorm:"size:64;not null"`
}

func (Usage) TableName() string { return "sb_usage" }

type Recharge struct {
	ID             int64  `json:"id" gorm:"primaryKey"`
	SupplierID     int64  `json:"supplier_id" gorm:"index;not null"`
	Date           string `json:"date" gorm:"size:10;index;not null"`
	Amount         string `json:"amount" gorm:"size:32;not null"`
	Currency       string `json:"currency" gorm:"size:3;not null"`
	Reference      string `json:"reference" gorm:"size:200;not null"`
	Note           string `json:"note" gorm:"type:text"`
	CreatedBy      int64  `json:"created_by"`
	CreatedAt      int64  `json:"created_at"`
	Reversed       bool   `json:"reversed"`
	IdempotencyKey string `json:"-" gorm:"size:64;uniqueIndex;not null"`
	PayloadHash    string `json:"-" gorm:"size:64;not null"`
}

func (Recharge) TableName() string { return "sb_recharges" }

// InitialBalance is the prepaid amount left with a supplier before this ledger
// started; negative means an unpaid balance. Each correction is a new revision,
// and the unique revision lets only one concurrent edit become current.
type InitialBalance struct {
	ID         int64  `json:"id" gorm:"primaryKey"`
	SupplierID int64  `json:"supplier_id" gorm:"uniqueIndex:sb_initial_revision;not null"`
	Revision   int64  `json:"revision" gorm:"uniqueIndex:sb_initial_revision;not null"`
	Date       string `json:"date" gorm:"size:10;not null"`
	Amount     string `json:"amount" gorm:"size:32;not null"`
	Note       string `json:"note" gorm:"type:text"`
	CreatedBy  int64  `json:"created_by"`
	CreatedAt  int64  `json:"created_at"`
}

func (InitialBalance) TableName() string { return "sb_initial_balances" }

// Claim is the supplier's own figure for one product line and day. It is a
// reconciliation reference only and never posts to the ledger.
type Claim struct {
	ID         int64  `json:"id" gorm:"primaryKey"`
	SupplierID int64  `json:"supplier_id" gorm:"uniqueIndex:sb_claim_line;not null"`
	Date       string `json:"date" gorm:"size:10;uniqueIndex:sb_claim_line;not null"`
	UsageType  string `json:"usage_type" gorm:"size:120;uniqueIndex:sb_claim_line;not null"`
	Quantity   string `json:"quantity" gorm:"size:32;not null"`
	Amount     string `json:"amount" gorm:"size:32;not null"`
	Note       string `json:"note" gorm:"type:text"`
	CreatedBy  int64  `json:"created_by"`
	CreatedAt  int64  `json:"created_at"`
}

func (Claim) TableName() string { return "sb_claims" }

// LedgerEntry is append-only; positive amounts are prepaid credits, negative
// amounts are charges. Decimal strings avoid dialect and binary-float rounding.
type LedgerEntry struct {
	ID         int64  `json:"id" gorm:"primaryKey"`
	SupplierID int64  `json:"supplier_id" gorm:"index;not null"`
	Date       string `json:"date" gorm:"size:10;index;not null"`
	Kind       string `json:"kind" gorm:"size:32;not null"`
	Amount     string `json:"amount" gorm:"size:32;not null"`
	UsageID    int64  `json:"usage_id" gorm:"index"`
	RechargeID int64  `json:"recharge_id" gorm:"index"`
	Note       string `json:"note" gorm:"type:text"`
	CreatedBy  int64  `json:"created_by"`
	CreatedAt  int64  `json:"created_at"`
}

func (LedgerEntry) TableName() string { return "sb_ledger" }
