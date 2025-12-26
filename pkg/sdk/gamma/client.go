package gamma

import (
	http2 "github.com/betbot/gobet/pkg/sdk/http"
	"math/big"
	"strings"
)

type Client struct {
	client *http2.Client

	chainId *big.Int
}

func NewClient(host string, chainId *big.Int) *Client {
	if strings.HasSuffix(host, "/") {
		host = host[:len(host)-1]
	}
	return &Client{
		client:  http2.NewClient(host),
		chainId: chainId,
	}
}
