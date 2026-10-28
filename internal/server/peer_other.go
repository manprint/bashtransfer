//go:build !linux

package server

import "net"

// Fuori da Linux la caduta di un mittente in attesa si rileva solo alla scadenza dell'attesa.
const peerCheckSupported = false

func peerClosed(net.Conn) bool { return false }
