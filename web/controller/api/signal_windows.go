//go:build windows

package api

import "errors"

func sendSelfSIGHUP() error {
	return errors.New("panel restart via signal is not supported on windows")
}
