package client

import (
	"encoding/json"
	"testing"
)

func TestBridgeCreateDepositResponse_Unmarshal(t *testing.T) {
	raw := []byte(`{
		"address":"0xabc",
		"depositAddresses":[
			{"chainId":"1","chainName":"Ethereum","tokenAddress":"0xusdc","tokenSymbol":"USDC","depositAddress":"0xdep"}
		]
	}`)
	var resp BridgeCreateDepositResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if resp.Address != "0xabc" {
		t.Fatalf("unexpected address: %s", resp.Address)
	}
	if len(resp.DepositAddresses) != 1 || resp.DepositAddresses[0].DepositAddress != "0xdep" {
		t.Fatalf("unexpected depositAddresses: %+v", resp.DepositAddresses)
	}
}

