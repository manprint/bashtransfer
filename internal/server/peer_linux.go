//go:build linux

package server

import (
	"net"
	"syscall"
	"unsafe"
)

const peerCheckSupported = true

// poll(2) constants on Linux; the syscall package does not export them.
const (
	pollErr   = 0x8
	pollHup   = 0x10
	pollRdHup = 0x2000
)

type pollFd struct {
	fd      int32
	events  int16
	revents int16
}

// peerClosed reports whether the client has closed the connection (FIN or RST). POLLRDHUP signals
// the close even with body bytes still unread in the receive buffer, which a read would only
// see after consuming them. A connection already closed by the server (srv.Close) counts
// as dropped. The call does not block (zero timeout).
func peerClosed(c net.Conn) bool {
	sc, ok := c.(syscall.Conn)
	if !ok {
		return false
	}
	raw, err := sc.SyscallConn()
	if err != nil {
		return false
	}
	closed := false
	err = raw.Control(func(fd uintptr) {
		pfd := pollFd{fd: int32(fd), events: pollRdHup}
		var ts syscall.Timespec
		n, _, errno := syscall.Syscall6(syscall.SYS_PPOLL, uintptr(unsafe.Pointer(&pfd)), 1,
			uintptr(unsafe.Pointer(&ts)), 0, 0, 0)
		closed = errno == 0 && n == 1 && pfd.revents&(pollRdHup|pollHup|pollErr) != 0
	})
	return closed || err != nil
}
