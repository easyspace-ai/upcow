package server

import (
	"fmt"
	"strings"

	hdwallet "github.com/miguelmota/go-ethereum-hdwallet"
)

type DerivedWallet struct {
	PrivateKeyHex string
	EOAAddress    string
}

func deriveWalletFromMnemonic(mnemonic string, derivationPath string) (*DerivedWallet, error) {
	mnemonic = strings.TrimSpace(mnemonic)
	derivationPath = strings.TrimSpace(derivationPath)
	if mnemonic == "" {
		return nil, fmt.Errorf("mnemonic is required")
	}
	if derivationPath == "" {
		return nil, fmt.Errorf("derivation_path is required")
	}

	w, err := hdwallet.NewFromMnemonic(mnemonic)
	if err != nil {
		return nil, fmt.Errorf("invalid mnemonic: %w", err)
	}

	path, err := hdwallet.ParseDerivationPath(derivationPath)
	if err != nil {
		return nil, fmt.Errorf("invalid derivation_path: %w", err)
	}

	acct, err := w.Derive(path, false)
	if err != nil {
		return nil, fmt.Errorf("derive failed: %w", err)
	}

	pk, err := w.PrivateKeyHex(acct)
	if err != nil {
		return nil, fmt.Errorf("private key failed: %w", err)
	}

	return &DerivedWallet{
		PrivateKeyHex: pk,
		EOAAddress:    strings.ToLower(acct.Address.Hex()),
	}, nil
}
