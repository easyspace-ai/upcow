package client

import (
	"fmt"

	"github.com/ethereum/go-ethereum/crypto"
)

// SignDigest signs an already-hashed digest with the client's private key.
// It returns the 65-byte signature (r,s,v) with Ethereum v adjusted to {27,28}.
//
// This is intentionally higher-level than exposing the raw private key.
func (c *Client) SignDigest(digest []byte) ([]byte, error) {
	if c == nil || c.authConfig == nil || c.authConfig.PrivateKey == nil {
		return nil, fmt.Errorf("private key not configured")
	}
	if len(digest) == 0 {
		return nil, fmt.Errorf("digest is empty")
	}
	sig, err := crypto.Sign(digest, c.authConfig.PrivateKey)
	if err != nil {
		return nil, err
	}
	// Adjust v value for Ethereum (add 27)
	if len(sig) == 65 && sig[64] < 27 {
		sig[64] += 27
	}
	return sig, nil
}

