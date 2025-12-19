package domain

// Market 市场领域模型
type Market struct {
	Slug        string // 市场 slug
	YesAssetID string // YES token 资产 ID
	NoAssetID  string // NO token 资产 ID
	ConditionID string // 条件 ID
	Question    string // 问题描述
	Timestamp   int64  // Unix 时间戳（秒）
}

// IsValid 验证市场是否有效
func (m *Market) IsValid() bool {
	return m.Slug != "" && m.YesAssetID != "" && m.NoAssetID != "" && m.Timestamp > 0
}

// GetAssetID 根据 token 类型获取资产 ID
func (m *Market) GetAssetID(tokenType TokenType) string {
	if tokenType == TokenTypeUp {
		return m.YesAssetID
	}
	return m.NoAssetID
}

// TokenType token 类型
type TokenType string

const (
	TokenTypeUp   TokenType = "up"
	TokenTypeDown TokenType = "down"
)

