// Package relay contiene il registry delle sessioni e il relay del body (D2, D3, D6–D8).
package relay

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"sync"
	"time"

	"bashtransfer/internal/slug"
)

var (
	ErrNotFound       = errors.New("slug not found")
	ErrClaimed        = errors.New("slug already claimed")
	ErrGone           = errors.New("slug concluded or expired")
	ErrCapacity       = errors.New("session capacity reached")
	ErrSlugCollision  = errors.New("slug collision")
	ErrSenderGone     = errors.New("sender gone")
	ErrReceiverGone   = errors.New("receiver gone")
	ErrLengthMismatch = errors.New("length mismatch")
	ErrIdle           = errors.New("sender idle")
	ErrExpired        = errors.New("wait timeout expired")
	// ErrInterrupted è l'errore di lettura del body dopo l'interruzione del relay (Stop).
	ErrInterrupted = errors.New("relay interrupted")
)

const (
	slugAttempts = 5
	// tombstoneTTL: per quanto uno slug terminale resta 410 (D17).
	tombstoneTTL = 24 * time.Hour
	// maxTombstones limita la memoria delle tombstone (circa 100 byte ciascuna): oltre il limite
	// la più vecchia viene scartata prima della scadenza e il suo slug risponde 404 invece di 410.
	// Senza limite, un client anonimo che cicla PUT e GET farebbe crescere la memoria con il ritmo
	// delle richieste per 24 ore.
	maxTombstones = 100000
)

// Receiver è il destinatario del relay: riceve i byte e fa flush per blocco.
type Receiver interface {
	io.Writer
	Flush()
}

// Session è una sessione di upload in attesa di un ricevente.
// Slug, ID, Name, Length e Created sono immutabili; gli altri campi sono protetti da reg.mu.
type Session struct {
	Slug string
	// ID identifica la sessione nei log. È casuale e indipendente dallo slug: chi legge i log
	// non può ricavarne l'URL di download.
	ID      string
	Name    string
	Length  int64 // -1 se ignota
	Created time.Time

	reg      *Registry
	claimed  bool
	err      error
	timer    *time.Timer
	attached chan Receiver
	expired  chan struct{}
	finished chan struct{}
	aborted  chan struct{}

	finishOnce sync.Once
}

// Attached riceve il ricevente quando uno slug viene reclamato.
func (s *Session) Attached() <-chan Receiver { return s.attached }

// Expired si chiude se nessun ricevente si aggancia entro il timeout di attesa.
func (s *Session) Expired() <-chan struct{} { return s.expired }

// Finished si chiude quando il mittente ha terminato (Registry.Finish).
func (s *Session) Finished() <-chan struct{} { return s.finished }

// Aborted si chiude alla prima chiamata di Abort: il mittente può interrompere una lettura
// bloccata invece di attendere il prossimo byte o BASHT_IDLE_TIMEOUT.
func (s *Session) Aborted() <-chan struct{} { return s.aborted }

// Err restituisce il primo errore registrato con Abort, o nil.
func (s *Session) Err() error {
	s.reg.mu.Lock()
	defer s.reg.mu.Unlock()
	return s.err
}

// Abort registra err solo alla prima chiamata e chiude Aborted.
func (s *Session) Abort(err error) {
	s.reg.mu.Lock()
	defer s.reg.mu.Unlock()
	if s.err == nil {
		s.err = err
		close(s.aborted)
	}
}

// Registry è l'unico punto che muta lo stato delle sessioni.
type Registry struct {
	mu          sync.Mutex
	sessions    map[string]*Session
	gone        map[string]struct{}
	goneQ       []tombstone
	maxGone     int
	maxSessions int
	waitTimeout time.Duration
	gen         func() (string, error)
}

type tombstone struct {
	slug string
	at   time.Time
}

// NewRegistry crea un registry con capacità e timeout di attesa dati.
func NewRegistry(maxSessions int, waitTimeout time.Duration) *Registry {
	return &Registry{
		sessions:    make(map[string]*Session),
		gone:        make(map[string]struct{}),
		maxGone:     maxTombstones,
		maxSessions: maxSessions,
		waitTimeout: waitTimeout,
		gen:         slug.New,
	}
}

// Create apre una sessione per un upload. length è -1 se ignota.
func (r *Registry) Create(name string, length int64) (*Session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.prune(time.Now())
	if len(r.sessions) >= r.maxSessions {
		return nil, ErrCapacity
	}
	for i := 0; i < slugAttempts; i++ {
		id, err := r.gen()
		if err != nil {
			return nil, err
		}
		if _, live := r.sessions[id]; live || r.isGone(id) {
			continue
		}
		logID, err := newLogID()
		if err != nil {
			return nil, err
		}
		s := &Session{
			Slug:     id,
			ID:       logID,
			Name:     name,
			Length:   length,
			Created:  time.Now(),
			reg:      r,
			attached: make(chan Receiver, 1),
			expired:  make(chan struct{}),
			finished: make(chan struct{}),
			aborted:  make(chan struct{}),
		}
		r.sessions[id] = s
		s.timer = time.AfterFunc(r.waitTimeout, func() { r.expire(s) })
		return s, nil
	}
	return nil, ErrSlugCollision
}

// newLogID genera l'identificativo di sessione per i log: 8 byte casuali in esadecimale.
func newLogID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// Claim assegna il ricevente allo slug. Esattamente un claim riesce per sessione.
func (r *Registry) Claim(id string, rcv Receiver) (*Session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.prune(time.Now())
	s, ok := r.sessions[id]
	if !ok {
		if r.isGone(id) {
			return nil, ErrGone
		}
		return nil, ErrNotFound
	}
	if s.claimed {
		return nil, ErrClaimed
	}
	s.claimed = true
	s.timer.Stop()
	s.attached <- rcv
	return s, nil
}

// Peek riporta lo stato di uno slug senza reclamarlo: nil se attende un ricevente, altrimenti
// ErrClaimed, ErrGone o ErrNotFound come Claim.
func (r *Registry) Peek(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.prune(time.Now())
	s, ok := r.sessions[id]
	switch {
	case !ok && r.isGone(id):
		return ErrGone
	case !ok:
		return ErrNotFound
	case s.claimed:
		return ErrClaimed
	}
	return nil
}

// Finish termina la sessione: rimuove dal registry una sola volta e chiude Finished.
// Deve essere chiamata solo dalla goroutine del mittente, dopo l'ultima scrittura al ricevente.
func (r *Registry) Finish(s *Session) {
	r.mu.Lock()
	if r.sessions[s.Slug] == s {
		delete(r.sessions, s.Slug)
		r.bury(s.Slug)
		s.timer.Stop()
	}
	r.mu.Unlock()
	s.finishOnce.Do(func() { close(s.finished) })
}

// Len restituisce il numero di sessioni vive.
func (r *Registry) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.sessions)
}

// ExpireWaiting fa scadere subito tutte le sessioni non ancora reclamate. Serve all'arresto:
// senza nuovi riceventi possibili, i mittenti in attesa vengono chiusi invece di occupare il
// periodo di grazia. I trasferimenti in corso non sono toccati.
func (r *Registry) ExpireWaiting() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, s := range r.sessions {
		r.expireLocked(s)
	}
}

// expire rimuove una sessione non reclamata allo scadere del timeout. Idempotente.
func (r *Registry) expire(s *Session) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.expireLocked(s)
}

func (r *Registry) expireLocked(s *Session) {
	if s.claimed || r.sessions[s.Slug] != s {
		return
	}
	s.timer.Stop()
	delete(r.sessions, s.Slug)
	r.bury(s.Slug)
	close(s.expired)
}

// bury registra lo slug come terminale per tombstoneTTL (D17), entro maxGone tombstone.
func (r *Registry) bury(id string) {
	r.gone[id] = struct{}{}
	r.goneQ = append(r.goneQ, tombstone{slug: id, at: time.Now()})
	for len(r.goneQ) > r.maxGone {
		r.dropOldest()
	}
}

// dropOldest rimuove la tombstone più vecchia e libera il riferimento alla stringa nell'array.
func (r *Registry) dropOldest() {
	delete(r.gone, r.goneQ[0].slug)
	r.goneQ[0] = tombstone{}
	r.goneQ = r.goneQ[1:]
}

func (r *Registry) isGone(id string) bool {
	_, ok := r.gone[id]
	return ok
}

// prune rimuove le tombstone scadute dalla testa della coda (FIFO per data).
func (r *Registry) prune(now time.Time) {
	for len(r.goneQ) > 0 && now.Sub(r.goneQ[0].at) >= tombstoneTTL {
		r.dropOldest()
	}
}
