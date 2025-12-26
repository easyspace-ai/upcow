package types

import "math/big"

type ContractConfig struct {
	SafeFactory   string `json:"safe_factory"`
	SafeMultisend string `json:"safe_multisend"`
}

func GetContractConfig(chainId *big.Int) *ContractConfig {
	switch chainId.Int64() {
	case 137:
		return &ContractConfig{
			SafeFactory:   "0xaacFeEa03eb1561C4e67d661e40682Bd20E3541b",
			SafeMultisend: "0xA238CBeb142c10Ef7Ad8442C6D1f9E89e07e7761",
		}
	default:
		panic("invalid chain id")
	}
}
