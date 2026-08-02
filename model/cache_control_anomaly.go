package model

// CacheControlAnomaly records requests where the client did NOT send a
// cache_control field but the upstream still reported cache creation tokens.
// It exists purely for diagnostics and is written only when the anomaly occurs,
// so the table stays small. The RequestId matches the consume log's request_id
// column, allowing the two to be joined externally.
type CacheControlAnomaly struct {
	Id                  int    `json:"id" gorm:"primaryKey"`
	CreatedAt           int64  `json:"created_at" gorm:"bigint;index:idx_cache_anomaly_created"`
	RequestId           string `json:"request_id" gorm:"type:varchar(64);index:idx_cache_anomaly_request_id"`
	UserId              int    `json:"user_id" gorm:"index:idx_cache_anomaly_user"`
	TokenId             int    `json:"token_id" gorm:"index:idx_cache_anomaly_token"`
	TokenName           string `json:"token_name" gorm:"type:varchar(255)"`
	ChannelId           int    `json:"channel_id" gorm:"index:idx_cache_anomaly_channel"`
	ModelName           string `json:"model_name" gorm:"type:varchar(255)"`
	Group               string `json:"group" gorm:"column:group;type:varchar(64)"`
	CacheCreationTokens int    `json:"cache_creation_tokens"`
	CacheReadTokens     int    `json:"cache_read_tokens"`
}

func (CacheControlAnomaly) TableName() string {
	return "cache_control_anomalies"
}

// RecordCacheControlAnomaly inserts one anomaly row. Errors are returned to the
// caller, which logs them; a failed insert must never affect billing.
func RecordCacheControlAnomaly(record *CacheControlAnomaly) error {
	if record == nil {
		return nil
	}
	return DB.Create(record).Error
}
