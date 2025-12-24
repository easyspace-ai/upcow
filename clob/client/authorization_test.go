package client

import (
	"strings"
	"testing"

	"github.com/betbot/gobet/clob/types"
)

func TestAuthorizationService_DefaultTargets_Polygon(t *testing.T) {
	cfg, err := GetContractConfig(types.ChainPolygon)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	// 基本 sanity：地址应为 0x 开头且长度合理
	check := func(name, addr string) {
		if !strings.HasPrefix(addr, "0x") || len(addr) < 10 {
			t.Fatalf("bad %s addr: %q", name, addr)
		}
	}
	check("exchange", cfg.Exchange)
	check("negRiskExchange", cfg.NegRiskExchange)
	check("negRiskAdapter", cfg.NegRiskAdapter)
	check("collateral", cfg.Collateral)
	check("conditionalTokens", cfg.ConditionalTokens)
}

