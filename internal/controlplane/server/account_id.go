package server

import (
	"fmt"
	"regexp"
)

var accountIDRe = regexp.MustCompile(`^\d{3}$`)

func normalizeAccountID(id string) (string, error) {
	if !accountIDRe.MatchString(id) {
		return "", fmt.Errorf("account_id must be 3 digits (e.g. 456)")
	}
	return id, nil
}

// derivationPathFromAccountID maps "456" -> "m/44'/60'/4'/5/6"
func derivationPathFromAccountID(id string) (string, error) {
	id, err := normalizeAccountID(id)
	if err != nil {
		return "", err
	}
	d0, d1, d2 := id[0], id[1], id[2]
	return fmt.Sprintf("m/44'/60'/%c'/%c/%c", d0, d1, d2), nil
}
