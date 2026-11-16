package agent

import "syscall"

// bindControl pins a socket to one network card (SO_BINDTODEVICE; the agent runs as root).
func bindControl(iface string) func(network, address string, c syscall.RawConn) error {
	if iface == "" {
		return nil
	}
	return func(_, _ string, c syscall.RawConn) error {
		var serr error
		if err := c.Control(func(fd uintptr) { serr = syscall.BindToDevice(int(fd), iface) }); err != nil {
			return err
		}
		return serr
	}
}
