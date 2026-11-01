// Package relay contiene il registry delle sessioni e il relay del body (D2, D3, D6–D8).
package relay

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"io"
	"sync"
	"sync/atomic"
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
	ErrSenderAway     = errors.New("sender not available")
	ErrReceiverGone   = errors.New("receiver gone")
	ErrLengthMismatch = errors.New("length mismatch")
	ErrIdle           = errors.New("sender idle")
	ErrExpired        = errors.New("wait timeout expired")
	// ErrInterrupted è l'errore di lettura del body dopo l'interruzione del relay (Stop).
	ErrInterrupted = errors.New("relay interrupted")
)

// Motivi per cui uno slug è terminale (GoneError): il ricevente vede un messaggio diverso per
// ciascuno, e il log distingue un link usato da uno perso per colpa del mittente.
const (
	GoneUsed       = "used"        // reclamato da un ricevente: trasferimento concluso o interrotto
	GoneExpired    = "expired"     // nessun ricevente entro BASHT_WAIT_TIMEOUT, o arresto del server
	GoneSenderGone = "sender_gone" // il mittente è caduto o ha annullato prima che un ricevente arrivasse
)

// GoneError è ErrGone con il motivo: errors.Is(err, ErrGone) vale per ogni GoneError.
type GoneError struct{ Reason string }

func (e *GoneError) Error() string { return ErrGone.Error() }

func (e *GoneError) Is(target error) bool { return target == ErrGone }

const (
	slugAttempts          = 5
	senderPresenceTimeout = 15 * time.Second
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
// Slug, ID, Name, Length, Token e Created sono immutabili; gli altri campi sono protetti da reg.mu.
type Session struct {
	Slug string
	// ID identifica la sessione nei log. È casuale e indipendente dallo slug: chi legge i log
	// non può ricavarne l'URL di download.
	ID     string
	Name   string
	Length int64 // -1 se ignota
	// Token autorizza l'invio del body di una sessione prenotata (Reserve); vuoto per Create.
	Token   string
	Created time.Time

	reg     *Registry
	claimed bool
	bound   bool // prenotata: il mittente ha agganciato il body (Bind)
	// unbind si chiude quando l'aggancio corrente termina: Detach del mittente caduto o aggancio
	// sostitutivo (BindDetachable). Un aggancio nuovo ne crea uno nuovo.
	unbind chan struct{}
	// streams conta gli stream di avanzamento aperti dal mittente nel browser (StreamOpen).
	streams            int
	monitored          bool
	foreground         bool
	receiverSeen       time.Time
	presenceTimer      *time.Timer
	presenceGeneration uint64
	err                error
	timer              *time.Timer
	bindTimer          *time.Timer
	bindGeneration     uint64
	attached           chan Receiver
	expired            chan struct{}
	finished           chan struct{}
	aborted            chan struct{}

	finishOnce sync.Once

	// Avanzamento per il mittente nel browser: inizio del relay, byte consegnati al ricevente,
	// esito. Senza reg.mu: li scrive la goroutine del mittente e li legge lo stream della
	// prenotazione.
	started   chan struct{}
	startOnce sync.Once
	delivered atomic.Int64
	result    atomic.Value // string
}

// Start segnala che il relay verso il ricevente è iniziato: chiude Started.
func (s *Session) Start() { s.startOnce.Do(func() { close(s.started) }) }

// Started si chiude quando il relay verso il ricevente inizia.
func (s *Session) Started() <-chan struct{} { return s.started }

// AddDelivered somma n ai byte consegnati al ricevente.
func (s *Session) AddDelivered(n int64) { s.delivered.Add(n) }

// Delivered restituisce i byte consegnati al ricevente.
func (s *Session) Delivered() int64 { return s.delivered.Load() }

// SetResult registra l'esito per il mittente nel browser. Va chiamata prima di Finish.
func (s *Session) SetResult(res string) { s.result.Store(res) }

// Result restituisce l'esito registrato con SetResult, o "".
func (s *Session) Result() string {
	res, _ := s.result.Load().(string)
	return res
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
	gone        map[string]string // slug terminale -> motivo (Gone*)
	goneQ       []tombstone
	maxGone     int
	maxSessions int
	waitTimeout time.Duration
	gen         func() (string, error)
	// senderGrace, se > 0, tiene viva per quel tempo una prenotazione il cui mittente è caduto
	// prima del ricevente: può riagganciarsi con lo stesso token (Detach, BindDetachable).
	senderGrace time.Duration
	// onGraceExpired, se non nil, è chiamata (senza lock) quando la tolleranza scade e chiude il link.
	onGraceExpired func(*Session)
}

type tombstone struct {
	slug string
	at   time.Time
}

// NewRegistry crea un registry con capacità e timeout di attesa dati.
func NewRegistry(maxSessions int, waitTimeout time.Duration) *Registry {
	return &Registry{
		sessions:    make(map[string]*Session),
		gone:        make(map[string]string),
		maxGone:     maxTombstones,
		maxSessions: maxSessions,
		waitTimeout: waitTimeout,
		gen:         slug.New,
	}
}

// SetSenderGrace imposta la tolleranza per i mittenti dal browser caduti prima del ricevente; 0 la
// disattiva. Va chiamata prima di servire richieste.
func (r *Registry) SetSenderGrace(d time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.senderGrace = d
}

// SenderGrace restituisce la tolleranza impostata con SetSenderGrace.
func (r *Registry) SenderGrace() time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.senderGrace
}

// OnGraceExpired registra la funzione chiamata quando la tolleranza scade e il link si chiude.
func (r *Registry) OnGraceExpired(fn func(*Session)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.onGraceExpired = fn
}

// Create apre una sessione per un upload. length è -1 se ignota.
func (r *Registry) Create(name string, length int64) (*Session, error) {
	return r.create(name, length, "", 0)
}

// Reserve apre una sessione il cui body arriva con una seconda richiesta: l'invio dal browser, che
// deve mostrare il link prima di iniziare l'upload. Il mittente aggancia il body con Bind e il
// Token della sessione entro bindTimeout; altrimenti la sessione termina come se il mittente fosse
// caduto (ErrSenderGone), anche con un ricevente già agganciato.
func (r *Registry) Reserve(name string, length int64, bindTimeout time.Duration) (*Session, error) {
	token, err := newToken()
	if err != nil {
		return nil, err
	}
	return r.create(name, length, token, bindTimeout)
}

// Bind assegna al mittente la sessione prenotata id. Uno slug sconosciuto, un token errato o una
// sessione già agganciata danno ErrNotFound: chi non ha il token non distingue i casi.
func (r *Registry) Bind(id, token string) (*Session, error) {
	s, _, err := r.BindDetachable(id, token)
	return s, err
}

// BindDetachable è Bind e restituisce anche il canale che si chiude quando questo aggancio termina
// senza chiudere la sessione: il mittente caduto (Detach) o un aggancio che lo sostituisce. Con la
// tolleranza attiva (SetSenderGrace) il mittente che torna con il token riaggancia la sessione anche
// se il server non ha ancora visto cadere il vecchio aggancio, purché il relay non sia iniziato e la
// sessione non sia stata interrotta: il vecchio aggancio viene chiuso e il suo gestore esce senza
// terminare la sessione.
func (r *Registry) BindDetachable(id, token string) (*Session, <-chan struct{}, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.sessions[id]
	if !ok || s.Token == "" || subtle.ConstantTimeCompare([]byte(s.Token), []byte(token)) != 1 {
		return nil, nil, ErrNotFound
	}
	if s.bound {
		if r.senderGrace <= 0 || s.err != nil || s.relayStarted() {
			return nil, nil, ErrNotFound
		}
		close(s.unbind)
	}
	s.bound = true
	s.bindGeneration++
	s.bindTimer.Stop()
	s.unbind = make(chan struct{})
	return s, s.unbind, nil
}

// relayStarted riporta se il relay verso il ricevente è iniziato (Start).
func (s *Session) relayStarted() bool {
	select {
	case <-s.started:
		return true
	default:
		return false
	}
}

// Away riporta se il body non è agganciato o la pagina che gestisce la presenza è assente.
func (s *Session) Away() bool {
	s.reg.mu.Lock()
	defer s.reg.mu.Unlock()
	return s.awayLocked()
}

func (s *Session) awayLocked() bool {
	return s.Token != "" && (!s.bound || (s.monitored && !s.foreground))
}

// ReceiverPresenceTimeout limita la validità della conferma della pagina ricevente.
const ReceiverPresenceTimeout = 5 * time.Second

func (s *Session) peerReadyLocked(now time.Time) bool {
	return s.reg.sessions[s.Slug] == s && s.err == nil && !s.awayLocked() &&
		(s.relayStarted() || (!s.claimed && !s.receiverSeen.IsZero() && now.Sub(s.receiverSeen) < ReceiverPresenceTimeout))
}

// PeerReady conferma il mittente e il ricevente, senza leggere o conservare il file.
func (s *Session) PeerReady() bool {
	s.reg.mu.Lock()
	defer s.reg.mu.Unlock()
	return s.peerReadyLocked(time.Now())
}

// PeerStatus è una fotografia atomica delle connessioni della sessione.
type PeerStatus struct {
	Ready, Away, Claimed, Started bool
}

// ObserveReceiver rinnova la presenza della pagina senza reclamare il link.
func (r *Registry) ObserveReceiver(id string) (PeerStatus, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	r.prune(now)
	s, ok := r.sessions[id]
	if !ok {
		if reason, gone := r.goneReason(id); gone {
			return PeerStatus{}, &GoneError{reason}
		}
		return PeerStatus{}, ErrNotFound
	}
	s.receiverSeen = now
	return PeerStatus{
		Ready: s.peerReadyLocked(now), Away: s.awayLocked(),
		Claimed: s.claimed, Started: s.relayStarted(),
	}, nil
}

// BeginRelay rende atomici l'inizio della copia e la sostituzione del PUT: un vecchio
// aggancio non può iniziare a leggere il file dopo che Resume lo ha staccato.
func (r *Registry) BeginRelay(s *Session, binding <-chan struct{}) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if s.Token != "" && (!s.bound || s.unbind != binding) {
		return false
	}
	s.Start()
	return true
}

// Foreground registra la disponibilità della pagina, senza leggere il body del file.
func (r *Registry) Foreground(id, token string, visible bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.sessions[id]
	if !ok || s.Token == "" || subtle.ConstantTimeCompare([]byte(s.Token), []byte(token)) != 1 {
		return ErrNotFound
	}
	r.foregroundLocked(s, visible)
	return nil
}

func (r *Registry) foregroundLocked(s *Session, visible bool) {
	s.monitored, s.foreground = true, visible
	s.presenceGeneration++
	if s.presenceTimer != nil {
		s.presenceTimer.Stop()
	}
	if !visible {
		r.detachLocked(s, s.unbind)
		return
	}
	generation := s.presenceGeneration
	s.presenceTimer = time.AfterFunc(senderPresenceTimeout, func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.sessions[s.Slug] == s && s.presenceGeneration == generation {
			s.foreground = false
			r.detachLocked(s, s.unbind)
		}
	})
}

// Resume riapre il controllo della sessione. Rebind stacca il vecchio PUT solo prima
// del relay; durante la copia si recupera esclusivamente lo stream di avanzamento.
func (r *Registry) Resume(id, token string, rebind bool) (*Session, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.sessions[id]
	if !ok || s.Token == "" || subtle.ConstantTimeCompare([]byte(s.Token), []byte(token)) != 1 || r.senderGrace <= 0 {
		return nil, false, ErrNotFound
	}
	started := s.relayStarted()
	if rebind && !started {
		r.detachLocked(s, s.unbind)
	}
	if s.monitored {
		r.foregroundLocked(s, true)
	}
	return s, started, nil
}

// Requeue rimette in coda il ricevente letto da Attached da un gestore che cede la sessione a un
// aggancio sostitutivo prima di iniziare il relay: lo leggerà il nuovo gestore.
func (s *Session) Requeue(rcv Receiver) { s.attached <- rcv }

// StreamOpen registra un nuovo stream di avanzamento del mittente nel browser (prenotazione o
// ripresa). Ogni StreamOpen va chiuso con StreamClosed.
func (r *Registry) StreamOpen(s *Session) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s.streams++
}

// StreamClosed registra la chiusura del controllo. Per i client senza presenza, la chiusura
// dell'ultimo stream prima del relay avvia la tolleranza. Con presenza, il body è indipendente.
func (r *Registry) StreamClosed(s *Session) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if s.streams > 0 {
		s.streams--
	}
	if s.streams > 0 || !s.bound || s.monitored {
		return false
	}
	return r.detachLocked(s, s.unbind) && !s.bound
}

// Detach gestisce la caduta del mittente dal browser, dell'aggancio binding (da BindDetachable), prima
// dell'inizio del relay. Con la tolleranza attiva la sessione resta viva per senderGrace, senza
// mittente: un ricevente che la reclama attende, e il mittente può riagganciarsi con lo stesso token
// (BindDetachable). Alla scadenza il link si chiude come per un mittente caduto. Riporta true se la
// sessione non va terminata dal chiamante: già ceduta a un altro aggancio, già in tolleranza o
// messa in tolleranza ora. False se il chiamante deve terminarla (Abort, Finish): tolleranza
// disattivata, mittente non prenotato, relay iniziato, sessione già finita o interrotta.
func (r *Registry) Detach(s *Session, binding <-chan struct{}) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.detachLocked(s, binding)
}

// detachLocked è Detach con r.mu preso.
func (r *Registry) detachLocked(s *Session, binding <-chan struct{}) bool {
	if r.senderGrace <= 0 || s.Token == "" || s.err != nil || r.sessions[s.Slug] != s || s.relayStarted() {
		return false
	}
	if s.unbind != binding || !s.bound {
		return true
	}
	s.bound = false
	close(s.unbind)
	s.bindGeneration++
	generation := s.bindGeneration
	s.bindTimer.Stop()
	s.bindTimer = time.AfterFunc(r.senderGrace, func() {
		if r.unbound(s, generation) {
			r.mu.Lock()
			fn := r.onGraceExpired
			r.mu.Unlock()
			if fn != nil {
				fn(s)
			}
		}
	})
	return true
}

// Reserved restituisce la sessione prenotata id, agganciata o no, se token è il suo: serve
// all'annullamento dal browser. Come per Bind, ogni altro caso è ErrNotFound.
func (r *Registry) Reserved(id, token string) (*Session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.sessions[id]
	if !ok || s.Token == "" || subtle.ConstantTimeCompare([]byte(s.Token), []byte(token)) != 1 {
		return nil, ErrNotFound
	}
	return s, nil
}

// unbound ignora i callback di un aggancio sostituito; generation 0 è l'annullamento esplicito.
func (r *Registry) unbound(s *Session, generation uint64) bool {
	r.mu.Lock()
	if s.bound || r.sessions[s.Slug] != s || (generation != 0 && generation != s.bindGeneration) {
		r.mu.Unlock()
		return false
	}
	if s.err == nil {
		s.err = ErrSenderGone
		close(s.aborted)
	}
	s.timer.Stop()
	s.bindTimer.Stop()
	if s.presenceTimer != nil {
		s.presenceTimer.Stop()
	}
	delete(r.sessions, s.Slug)
	r.bury(s.Slug, GoneSenderGone)
	r.mu.Unlock()
	s.finishOnce.Do(func() { close(s.finished) })
	return true
}

// Cancel termina la sessione prenotata s perché il mittente nel browser ha chiuso la pagina o
// annullato: prima del Bind come allo scadere di bindTimeout, dopo con Abort(ErrSenderGone), che
// sveglia il mittente in attesa del ricevente. Su una sessione finita non fa nulla.
func (r *Registry) Cancel(s *Session) {
	select {
	case <-s.finished:
		return
	default:
	}
	if !r.unbound(s, 0) {
		s.Abort(ErrSenderGone)
	}
}

func (r *Registry) create(name string, length int64, token string, bindTimeout time.Duration) (*Session, error) {
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
		_, gone := r.goneReason(id)
		if _, live := r.sessions[id]; live || gone {
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
			Token:    token,
			Created:  time.Now(),
			reg:      r,
			attached: make(chan Receiver, 1),
			expired:  make(chan struct{}),
			finished: make(chan struct{}),
			aborted:  make(chan struct{}),
			started:  make(chan struct{}),
		}
		r.sessions[id] = s
		s.timer = time.AfterFunc(r.waitTimeout, func() { r.expire(s) })
		if token != "" {
			s.bindGeneration = 1
			s.bindTimer = time.AfterFunc(bindTimeout, func() { r.unbound(s, 1) })
		}
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

// newToken genera il token di una sessione prenotata: 16 byte casuali in esadecimale.
func newToken() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// Claim assegna il ricevente allo slug. Esattamente un claim riesce per sessione.
func (r *Registry) Claim(id string, rcv Receiver) (*Session, error) {
	return r.claim(id, rcv, false)
}

// ClaimReady lascia il link intatto quando il browser deve attendere il mittente.
func (r *Registry) ClaimReady(id string, rcv Receiver) (*Session, error) {
	return r.claim(id, rcv, true)
}

func (r *Registry) claim(id string, rcv Receiver, requireReady bool) (*Session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.prune(time.Now())
	s, ok := r.sessions[id]
	if !ok {
		if reason, gone := r.goneReason(id); gone {
			return nil, &GoneError{reason}
		}
		return nil, ErrNotFound
	}
	if s.claimed {
		return nil, ErrClaimed
	}
	if requireReady && s.awayLocked() {
		return nil, ErrSenderAway
	}
	s.claimed = true
	s.timer.Stop()
	s.attached <- rcv
	return s, nil
}

// Peek riporta lo stato di uno slug senza reclamarlo: la sessione se attende un ricevente,
// altrimenti ErrClaimed, un GoneError (ErrGone) o ErrNotFound come Claim. Della sessione il chiamante legge solo
// i campi immutabili (nome e lunghezza per la pagina di download).
func (r *Registry) Peek(id string) (*Session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.prune(time.Now())
	s, ok := r.sessions[id]
	if !ok {
		if reason, gone := r.goneReason(id); gone {
			return nil, &GoneError{reason}
		}
		return nil, ErrNotFound
	}
	if s.claimed {
		return nil, ErrClaimed
	}
	return s, nil
}

// Finish termina la sessione: rimuove dal registry una sola volta e chiude Finished.
// Deve essere chiamata solo dalla goroutine del mittente, dopo l'ultima scrittura al ricevente.
func (r *Registry) Finish(s *Session) {
	r.mu.Lock()
	if r.sessions[s.Slug] == s {
		delete(r.sessions, s.Slug)
		r.bury(s.Slug, finishReason(s))
		s.timer.Stop()
		if s.presenceTimer != nil {
			s.presenceTimer.Stop()
		}
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
	if s.presenceTimer != nil {
		s.presenceTimer.Stop()
	}
	delete(r.sessions, s.Slug)
	r.bury(s.Slug, GoneExpired)
	close(s.expired)
	// Una prenotazione senza body non ha un mittente che chiami Finish.
	if s.Token != "" && !s.bound {
		s.bindTimer.Stop()
		s.finishOnce.Do(func() { close(s.finished) })
	}
}

// finishReason è il motivo della tombstone di una sessione che termina (r.mu preso). Uno slug
// reclamato vale sempre GoneUsed, anche se poi il mittente è caduto: il ricevente ha già avuto il
// link. Senza ricevente, la scadenza è GoneExpired e ogni altra fine è un mittente caduto.
func finishReason(s *Session) string {
	switch {
	case s.claimed:
		return GoneUsed
	case errors.Is(s.err, ErrExpired):
		return GoneExpired
	}
	return GoneSenderGone
}

// bury registra lo slug come terminale per tombstoneTTL (D17), entro maxGone tombstone.
func (r *Registry) bury(id, reason string) {
	r.gone[id] = reason
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

// goneReason riporta il motivo della tombstone di id, se c'è.
func (r *Registry) goneReason(id string) (string, bool) {
	reason, ok := r.gone[id]
	return reason, ok
}

// prune rimuove le tombstone scadute dalla testa della coda (FIFO per data).
func (r *Registry) prune(now time.Time) {
	for len(r.goneQ) > 0 && now.Sub(r.goneQ[0].at) >= tombstoneTTL {
		r.dropOldest()
	}
}
