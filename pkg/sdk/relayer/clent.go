package relayer

import (
	"context"
	"encoding/json"
	"fmt"
	clobtypes "github.com/betbot/gobet/pkg/sdk/clob/types"
	sdkheaders "github.com/betbot/gobet/pkg/sdk/headers"
	http2 "github.com/betbot/gobet/pkg/sdk/http"
	"github.com/betbot/gobet/pkg/sdk/relayer/types"
	"github.com/betbot/gobet/pkg/sdk/signing"
	sdktypes "github.com/betbot/gobet/pkg/sdk/types"
	"github.com/pkg/errors"
	"math/big"
	"net/http"
	"strings"
	"time"
)

type Client struct {
	client *http2.Client

	chainId            *big.Int
	signFn             signing.SignatureFunc
	builderApiKeyCreds *sdktypes.BuilderApiKeyCreds

	contractConfig *types.ContractConfig
}

func NewClient(host string, chainId *big.Int, signFn signing.SignatureFunc, builderApiKeyCreds *sdktypes.BuilderApiKeyCreds) *Client {
	if strings.HasSuffix(host, "/") {
		host = host[:len(host)-1]
	}
	return &Client{
		client:             http2.NewClient(host),
		chainId:            chainId,
		builderApiKeyCreds: builderApiKeyCreds,
		signFn:             signFn,
		contractConfig:     types.GetContractConfig(chainId),
	}
}

func (c *Client) WithSignatureFunc(signFn signing.SignatureFunc) error {
	if c.signFn != nil {
		return errors.New("signFn already set")
	}
	c.signFn = signFn
	return nil
}

func (c *Client) GetNonce(signerAddress string, signerType types.TransactionType) (types.NoncePayload, error) {
	var resp types.NoncePayload
	res, err := c.client.DoRequest(http.MethodGet, types.GET_NONCE, &http2.RequestOptions{
		Params: map[string]any{
			"address": signerAddress,
			"type":    signerType},
	}, &resp)
	if _, e := http2.ParseHTTPError(res, err); e != nil {
		return types.NoncePayload{}, e
	}
	return resp, nil
}

// GetTransaction
func (c *Client) GetTransaction(transactionID string) ([]types.RelayerTransaction, error) {
	var resp []types.RelayerTransaction

	res, err := c.client.DoRequest(http.MethodGet, types.GET_TRANSACTION, &http2.RequestOptions{
		Params: map[string]any{
			"id": transactionID},
	}, &resp)
	if _, e := http2.ParseHTTPError(res, err); e != nil {
		return nil, e
	}
	return resp, nil
}

// GetTransactions
func (c *Client) GetTransactions() ([]types.RelayerTransaction, error) {
	requestOptions := &http2.RequestOptions{}
	if c.builderApiKeyCreds != nil {
		ts := time.Now().Unix()
		l2HeaderArgs := clobtypes.L2HeaderArgs{
			Method:      http.MethodGet,
			RequestPath: types.GET_TRANSACTIONS,
			Body:        ``,
		}
		builderHeaders, errBuilder := sdkheaders.CreateL2BuilderHeaders(c.builderApiKeyCreds, l2HeaderArgs, &ts)
		if errBuilder == nil && len(builderHeaders) != 0 {
			requestOptions.Headers = builderHeaders
		}
	}
	var resp []types.RelayerTransaction
	res, err := c.client.DoRequest(http.MethodGet, types.GET_TRANSACTIONS, requestOptions, &resp)
	if _, e := http2.ParseHTTPError(res, err); e != nil {
		return nil, e
	}
	return resp, nil
}

func (c *Client) Deploy(option *sdktypes.AuthOption) (*types.RelayerTransactionResponse, error) {
	safeAddr, err := c.GetExpectedSafe(option.SingerAddress)
	if err != nil {
		return nil, errors.WithMessagef(err, "deploy getExpectedSafe signer:%v", option.SingerAddress)
	}

	deployed, err := c.GetDeployed(safeAddr)
	if err != nil {
		return nil, fmt.Errorf("deploy: GetDeployed failed: %w", err)
	}
	if deployed.Deployed {
		return nil, errors.Errorf("deploy: Deployed already deployed. signer:%s, safeAddr:%s", option.SingerAddress, safeAddr)
	}

	fmt.Printf("Deploying safe %s...\n", safeAddr)

	resp, err := c.deploy(option)
	if err != nil {
		return nil, fmt.Errorf("deploy: deployInternal failed: %w", err)
	}
	return resp, nil
}

func (c *Client) GetExpectedSafe(ownerAddr string) (string, error) {
	safeFactoryAddr := c.contractConfig.SafeFactory
	safeAddr, err := deriveSafe(ownerAddr, safeFactoryAddr)
	if err != nil {
		return "", fmt.Errorf("getExpectedSafe: deriveSafe failed: %w", err)
	}
	return safeAddr, nil
}

func (c *Client) deploy(option *sdktypes.AuthOption) (*types.RelayerTransactionResponse, error) {
	start := time.Now()

	from := option.SingerAddress
	args := types.SafeCreateTransactionArgs{
		From:            from,
		ChainID:         c.chainId.Int64(),
		PaymentToken:    sdktypes.ZeroAddress,
		Payment:         "0",
		PaymentReceiver: sdktypes.ZeroAddress,
	}

	reqBody, err := buildSafeCreateTransactionRequest(c.chainId, c.signFn, c.contractConfig.SafeFactory, args)
	if err != nil {
		return nil, fmt.Errorf("deployInternal: build request failed: %w", err)
	}

	fmt.Printf("Client side deploy request creation took: %.3f seconds\n", time.Since(start).Seconds())

	payloadBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("deployInternal: marshal request failed: %w", err)
	}

	body := string(payloadBytes)

	headers, err := sdkheaders.CreateL2BuilderHeaders(c.builderApiKeyCreds, clobtypes.L2HeaderArgs{Method: http.MethodPost, RequestPath: types.SUBMIT_TRANSACTION, Body: body}, nil)
	if err != nil {
		return nil, fmt.Errorf("deploy internal: create header failed: %w", err)
	}

	var out types.RelayerTransactionResponse
	resp, err := c.client.DoRequest(http.MethodPost, types.SUBMIT_TRANSACTION, &http2.RequestOptions{
		Headers: headers,
		Data:    body,
	}, &out)
	if _, e := http2.ParseHTTPError(resp, err); e != nil {
		return nil, e
	}
	return &out, nil
}

func (c *Client) Execute(txns []types.SafeTransaction, metadata string, option *sdktypes.AuthOption) (*types.RelayerTransactionResponse, error) {
	start := time.Now()
	from := option.SingerAddress
	safeAddr, err := c.GetExpectedSafe(from)
	if err != nil {
		return nil, fmt.Errorf("execute: GetExpectedSafe failed: %w", err)
	}

	deployed, err := c.GetDeployed(safeAddr)
	if err != nil {
		return nil, fmt.Errorf("execute: GetDeployed failed: %w", err)
	}
	if !deployed.Deployed {
		return nil, fmt.Errorf("execute: safe not deployed")
	}

	noncePayload, err := c.GetNonce(from, types.TransactionTypeSAFE)
	if err != nil {
		return nil, fmt.Errorf("execute: GetNonce failed: %w", err)
	}

	args := types.SafeTransactionArgs{
		From:         from,
		Nonce:        noncePayload.Nonce,
		ChainID:      c.chainId.Int64(),
		Transactions: txns,
	}

	reqBody, err := buildSafeTransactionRequest(c.signFn, args, c.contractConfig, metadata)
	if err != nil {
		return nil, fmt.Errorf("execute: build safe transaction r	equest failed: %w", err)
	}

	fmt.Printf("Client side safe request creation took: %.3f seconds\n", time.Since(start).Seconds())

	payloadBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("execute: marshal request failed: %w", err)
	}

	body := string(payloadBytes)

	headers, err := sdkheaders.CreateL2BuilderHeaders(c.builderApiKeyCreds, clobtypes.L2HeaderArgs{Method: http.MethodPost, RequestPath: types.SUBMIT_TRANSACTION, Body: body}, nil)
	if err != nil {
		return nil, fmt.Errorf("deploy internal: create header failed: %w", err)
	}

	var out types.RelayerTransactionResponse
	resp, err := c.client.DoRequest(http.MethodPost, types.SUBMIT_TRANSACTION, &http2.RequestOptions{
		Headers: headers,
		Data:    body,
	}, &out)
	if _, e := http2.ParseHTTPError(resp, err); e != nil {
		return nil, e
	}
	return &out, nil
}

func (c *Client) GetDeployed(safeAddr string) (types.GetDeployedResponse, error) {
	var resp types.GetDeployedResponse
	res, err := c.client.DoRequest(http.MethodGet, types.GET_DEPLOYED, &http2.RequestOptions{
		Params: map[string]any{
			"address": safeAddr},
	}, &resp)
	if _, e := http2.ParseHTTPError(res, err); e != nil {
		return types.GetDeployedResponse{}, e
	}
	return resp, nil
}

func (c *Client) PollUntilState(
	ctx context.Context,
	transactionID string,
	states []types.RelayerTransactionState,
	failState *types.RelayerTransactionState,
	maxPolls int,
	pollFreq time.Duration,
) (*types.RelayerTransaction, error) {

	if maxPolls <= 0 {
		maxPolls = 10
	}
	if pollFreq < time.Second {
		pollFreq = 2 * time.Second
	}

	contains := func(target types.RelayerTransactionState, set []types.RelayerTransactionState) bool {
		for _, s := range set {
			if s == target {
				return true
			}
		}
		return false
	}

	ticker := time.NewTicker(pollFreq)
	defer ticker.Stop()

	polls := 0
	for {
		txns, err := c.GetTransaction(transactionID)
		if err != nil {
			return nil, fmt.Errorf("get transaction failed: %w", err)
		}
		if len(txns) > 0 {
			txn := txns[0]
			if contains(txn.State, states) {
				return &txn, nil
			}
			if failState != nil && txn.State == *failState {
				return nil, fmt.Errorf("transaction %s failed on-chain (hash=%s)", transactionID, txn.TransactionHash)
			}
		}

		polls++
		if polls >= maxPolls {
			return nil, errors.New("transaction not found or not in given states (timeout)")
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}
