package server

import (
	"context"
	"net"
	"time"
)

// peerCheckInterval is how often a sender waiting for the receiver is checked: net/http
// does not detect its drop until the body is read, and the body is read only after the claim.
const peerCheckInterval = time.Second

// heartbeatInterval is how often, over HTTP/2, a waiting sender receives a "\r" byte. An h2c
// proxy (Apache) does not read the body until the backend asks for it and does not notice the
// dropped sender: by writing to it, the write fails and the proxy closes the stream. "\r" is not
// visible, because the commands end with "\n". Variable for tests.
var heartbeatInterval = 2 * time.Second

type connKey struct{}

// ConnContext must be set as http.Server.ConnContext: it makes the sender's connection
// available to the handler to check for a drop while waiting.
func ConnContext(ctx context.Context, c net.Conn) context.Context {
	return context.WithValue(ctx, connKey{}, c)
}

// connFrom returns the connection stored by ConnContext, or nil.
func connFrom(ctx context.Context) net.Conn {
	c, _ := ctx.Value(connKey{}).(net.Conn)
	return c
}
