package types

import (
	"github.com/polymarket/go-order-utils/pkg/model"
)

type Chain int64

const (
	POLYGON Chain = 137
	AMOY          = 80002

	ZeroAddress = "0x0000000000000000000000000000000000000000"
)

type AuthOption struct {
	SignatureType model.SignatureType `json:"signature_type"`
	SingerAddress string              `json:"singer_address"`
	FunderAddress string              `json:"funder_address"`

	ApiKeyCreds *ApiKeyCreds `json:"api_key_creds"`
}

// ApiKeyCreds API密钥凭证
type ApiKeyCreds struct {
	ApiKey     string `json:"apiKey"`
	Secret     string `json:"secret"`
	Passphrase string `json:"passphrase"`
}

// BuilderApiKeyCreds Builder API密钥
type BuilderApiKeyCreds struct {
	Key        string `json:"key" yaml:"key" mapstructure:"key"`
	Secret     string `json:"secret" yaml:"secret" mapstructure:"secret"`
	Passphrase string `json:"passphrase" yaml:"passphrase" mapstructure:"passphrase"`
}

// BuilderApiKeyResponse Builder API密钥响应
type BuilderApiKeyResponse struct {
	Key       string `json:"key"`
	CreatedAt string `json:"createdAt,omitempty"`
	RevokedAt string `json:"revokedAt,omitempty"`
}
