//go:build !windows

package api

import (
	"os"
	"syscall"
)

func sendSelfSIGHUP() error {
	p, err := os.FindProcess(os.Getpid())
	if err != nil {
		return err
	}
	return p.Signal(syscall.SIGHUP)
}
