package gamma_test

import (
	"github.com/betbot/gobet/pkg/sdk/gamma"
	"github.com/stretchr/testify/assert"
	"math/big"
	"testing"
)

var (
	PolymarketGammaURL = "https://gamma-api.polymarket.com"
	chaindId           = big.NewInt(137)
)

func TestGetPositions(t *testing.T) {
	client := gamma.NewClient(PolymarketGammaURL, chaindId)

	positions, err := client.GetMarketsBySlug("us-x-venezuela-military-engagement-by-september-30-659")
	assert.Equal(t, nil, err)
	t.Logf("positions: %v", positions)

}
