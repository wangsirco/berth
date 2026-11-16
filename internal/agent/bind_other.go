//go:build !linux

package agent

import "syscall"

func bindControl(string) func(network, address string, c syscall.RawConn) error { return nil }
