//go:build !linux

package server

import "net"

// Outside Linux, a waiting sender's drop is detected only when the wait expires.
const peerCheckSupported = false

func peerClosed(net.Conn) bool { return false }
