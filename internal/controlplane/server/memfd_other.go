//go:build !linux

package server

import (
	"fmt"
	"runtime"
)

func createMemfd(name string) (int, error) {
	return 0, fmt.Errorf("memfd not supported on %s", runtime.GOOS)
}
