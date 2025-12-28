//go:build linux

package server

import (
	"golang.org/x/sys/unix"
)

func createMemfd(name string) (int, error) {
	return unix.MemfdCreate(name, 0)
}
