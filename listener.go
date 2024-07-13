//go:build linux || darwin

package bahamut

import (
	"net"

	"github.com/valyala/tcplisten"
)

// MakeListener creates the net.Listener, optimized for the current platform.
// It uses valyala/tcplisten on Linux and macOS and a class net.NewListener on
// Windows
func MakeListener(network string, listen string) (net.Listener, error) {

	return (&tcplisten.Config{
		ReusePort:   true,
		DeferAccept: true,
		FastOpen:    true,
	}).NewListener(network, listen)
}
