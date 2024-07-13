//go:build windows

package bahamut

import (
	"net"
)

// MakeListener creates the net.Listener, optimized for the current platform.
// It uses valyala/tcplisten on Linux and macOS and a class net.NewListener on
// Windows
func MakeListener(network string, listen string) (net.Listener, error) {
	return net.Listen(network, listen)
}
