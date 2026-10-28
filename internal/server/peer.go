package server

import (
	"context"
	"net"
	"time"
)

// peerCheckInterval è ogni quanto un mittente in attesa del ricevente viene controllato: net/http
// non rileva la sua caduta finché il body non è letto, e il body si legge solo dopo il claim.
const peerCheckInterval = time.Second

type connKey struct{}

// ConnContext va impostato come http.Server.ConnContext: rende la connessione del mittente
// disponibile all'handler per il controllo della caduta durante l'attesa.
func ConnContext(ctx context.Context, c net.Conn) context.Context {
	return context.WithValue(ctx, connKey{}, c)
}

// connFrom restituisce la connessione salvata da ConnContext, o nil.
func connFrom(ctx context.Context) net.Conn {
	c, _ := ctx.Value(connKey{}).(net.Conn)
	return c
}
