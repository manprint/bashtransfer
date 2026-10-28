// Package relay contiene il registry delle sessioni e il relay del body (D2, D3, D6–D8).
package relay

import (
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
// Slug, Name e Length sono immutabili; gli altri campi sono protetti da reg.mu.
type Session struct {
	Slug   string
	Name   string
	Length int64 // -1 se ignota

	reg      *Registry
	claimed  bool
	err      error
	timer    *time.Timer
	attached chan Receiver
	expired  chan struct{}
	finished chan struct{}

	finishOnce sync.Once
}

// Attached riceve il ricevente quando uno slug viene reclamato.
func (s *Session) Attached() <-chan Receiver { return s.attached }

// Expired si chiude se nessun ricevente si aggancia entro il timeout di attesa.
func (s *Session) Expired() <-chan struct{} { return s.expired }

// Finished si chiude quando il mittente ha terminato (Registry.Finish).
func (s *Session) Finished() <-chan struct{} { return s.finished }

// Err restituisce il primo errore registrato con Abort, o nil.
func (s *Session) Err() error {
	s.reg.mu.Lock()
	defer s.reg.mu.Unlock()
	return s.err
}

// Abort registra err solo alla prima chiamata. Non chiude nulla.
func (s *Session) Abort(err error) {
	s.reg.mu.Lock()
	defer s.reg.mu.Unlock()
	if s.err == nil {
		s.err = err
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
		s := &Session{
			Slug:     id,
			Name:     name,
			Length:   length,
			reg:      r,
			attached: make(chan Receiver, 1),
			expired:  make(chan struct{}),
			finished: make(chan struct{}),
		}
		r.sessions[id] = s
		s.timer = time.AfterFunc(r.waitTimeout, func() { r.expire(s) })
		return s, nil
	}
	return nil, ErrSlugCollision
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

// expire rimuove una sessione non reclamata allo scadere del timeout. Idempotente.
func (r *Registry) expire(s *Session) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if s.claimed || r.sessions[s.Slug] != s {
		return
	}
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
