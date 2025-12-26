package headers

import (
	"fmt"
	"github.com/betbot/gobet/pkg/sdk/clob/types"
	"github.com/betbot/gobet/pkg/sdk/signing"
	sdktypes "github.com/betbot/gobet/pkg/sdk/types"
	"math/big"
	"time"
)

func CreateL1Headers(
	signerAddr string,
	signatureFunc signing.SignatureFunc,
	chainId *big.Int,
	nonce *big.Int,
	timestamp *int64,
) (map[string]string, error) {
	ts := time.Now().Unix()
	if timestamp != nil {
		ts = *timestamp
	}
	tsStr := fmt.Sprintf("%d", ts)

	n := big.NewInt(0)
	if nonce != nil {
		n = nonce
	}

	sig, err := signing.BuildClobEip712Signature(signatureFunc, chainId, signerAddr, tsStr, n)
	if err != nil {
		return nil, fmt.Errorf("create L1 signature: %w", err)
	}

	return map[string]string{
		"POLY_ADDRESS":   signerAddr,
		"POLY_SIGNATURE": sig,
		"POLY_TIMESTAMP": tsStr,
		"POLY_NONCE":     n.String(),
	}, nil
}

func CreateL2Headers(
	signerAddr string,
	creds *sdktypes.ApiKeyCreds,
	l2HeaderArgs types.L2HeaderArgs,
	tsOpt *int64,
) (map[string]string, error) {
	ts := time.Now().Unix()
	if tsOpt != nil {
		ts = *tsOpt
	}
	tsStr := fmt.Sprintf("%d", ts)
	sig, err := signing.BuildPolyHmacSignature(creds.Secret, tsStr, l2HeaderArgs.Method, l2HeaderArgs.RequestPath, &l2HeaderArgs.Body)
	if err != nil {
		return nil, fmt.Errorf("create L2 signature: %w", err)
	}
	return map[string]string{
		"POLY_ADDRESS":    signerAddr,
		"POLY_SIGNATURE":  sig,
		"POLY_TIMESTAMP":  tsStr,
		"POLY_API_KEY":    creds.ApiKey,
		"POLY_PASSPHRASE": creds.Passphrase,
	}, nil
}

func CreateL2BuilderHeaders(
	creds *sdktypes.BuilderApiKeyCreds,
	headerArgs types.L2HeaderArgs,
	tsOpt *int64,
) (map[string]string, error) {
	ts := time.Now().Unix()
	if tsOpt != nil {
		ts = *tsOpt
	}
	tsStr := fmt.Sprintf("%d", ts)
	sig, err := signing.BuildPolyHmacSignature(creds.Secret, tsStr, headerArgs.Method, headerArgs.RequestPath, &headerArgs.Body)
	if err != nil {
		return nil, fmt.Errorf("create L2 builder signature: %w", err)
	}
	return map[string]string{
		"POLY_BUILDER_SIGNATURE":  sig,
		"POLY_BUILDER_TIMESTAMP":  tsStr,
		"POLY_BUILDER_API_KEY":    creds.Key,
		"POLY_BUILDER_PASSPHRASE": creds.Passphrase,
	}, nil
}

func InjectBuilderHeaders(l2Header, builderHeaders map[string]string) map[string]string {
	result := make(map[string]string, len(l2Header)+len(builderHeaders))
	for k, v := range l2Header {
		result[k] = v
	}
	for k, v := range builderHeaders {
		result[k] = v
	}
	return result
}
