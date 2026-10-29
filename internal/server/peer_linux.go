//go:build linux

package server

import (
	"net"
	"syscall"
	"unsafe"
)

const peerCheckSupported = true

// Costanti di poll(2) su Linux; il pacchetto syscall non le esporta.
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

// peerClosed riporta se il client ha chiuso la connessione (FIN o RST). POLLRDHUP segnala la
// chiusura anche con byte del body ancora non letti nel buffer di ricezione, che una lettura
// vedrebbe solo dopo averli consumati. Una connessione già chiusa dal server (srv.Close) conta
// come caduta. La chiamata non blocca (timeout zero).
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
