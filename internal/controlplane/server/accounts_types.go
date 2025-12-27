package server

import "time"

type Account struct {
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	DerivationPath string    `json:"derivation_path"`
	EOAAddress     string    `json:"eoa_address"`
	FunderAddress  string    `json:"funder_address"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}
