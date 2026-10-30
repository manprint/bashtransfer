package server

import (
	"context"
	"net"
	"time"
)

// peerCheckInterval è ogni quanto un mittente in attesa del ricevente viene controllato: net/http
// non rileva la sua caduta finché il body non è letto, e il body si legge solo dopo il claim.
const peerCheckInterval = time.Second

// heartbeatInterval è ogni quanto, su HTTP/2, un mittente in attesa riceve un byte "\r". Un proxy
// h2c (Apache) non legge il body finché il backend non lo chiede e non si accorge del mittente
// caduto: scrivendogli, la scrittura fallisce e il proxy chiude lo stream. "\r" non è visibile,
// perché i comandi terminano con "\n". Variabile per i test.
var heartbeatInterval = 2 * time.Second

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
