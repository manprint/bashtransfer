// Package relay contains the session registry and the body relay (D2, D3, D6–D8).
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
	// ErrInterrupted is the body read error after the relay is interrupted (Stop).
	ErrInterrupted = errors.New("relay interrupted")
)

// Reasons why a slug is terminal (GoneError): the receiver sees a different message for each,
// and the log tells a used link apart from one lost through the sender.
const (
	GoneUsed       = "used"        // claimed by a receiver: transfer completed or interrupted
	GoneExpired    = "expired"     // no receiver within BASHT_WAIT_TIMEOUT, or server shutdown
	GoneSenderGone = "sender_gone" // the sender dropped or cancelled before a receiver arrived
)

// GoneError is ErrGone with the reason: errors.Is(err, ErrGone) holds for every GoneError.
type GoneError struct{ Reason string }

func (e *GoneError) Error() string { return ErrGone.Error() }

func (e *GoneError) Is(target error) bool { return target == ErrGone }

const (
	slugAttempts          = 5
	senderPresenceTimeout = 15 * time.Second
	// tombstoneTTL: how long a terminal slug keeps answering 410 (D17).
	tombstoneTTL = 24 * time.Hour
	// maxTombstones bounds the tombstone memory (about 100 bytes each): beyond the limit the oldest
	// is discarded before it expires and its slug answers 404 instead of 410. Without a limit, an
	// anonymous client cycling PUT and GET would grow memory at the request rate for 24 hours.
	maxTombstones = 100000
)

// Receiver is the relay's recipient: it receives the bytes and flushes per block.
type Receiver interface {
	io.Writer
	Flush()
}

// Session is an upload session waiting for a receiver.
// Slug, ID, Name, Length, Token and Created are immutable; the other fields are protected by reg.mu.
type Session struct {
	Slug string
	// ID identifies the session in the logs. It is random and independent of the slug: someone
	// reading the logs cannot derive the download URL from it.
	ID     string
	Name   string
	Length int64 // -1 if unknown
	// Token authorizes sending the body of a reserved session (Reserve); empty for Create.
	Token   string
	Created time.Time

	reg     *Registry
	claimed bool
	bound   bool // reserved: the sender attached the body (Bind)
	// unbind closes when the current attachment ends: Detach of a dropped sender or a replacing
	// attachment (BindDetachable). A new attachment creates a new one.
	unbind chan struct{}
	// streams counts the progress streams the sender opened in the browser (StreamOpen).
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

	// Progress for the sender in the browser: relay start, bytes delivered to the receiver,
	// outcome. Not under reg.mu: the sender's goroutine writes them and the reservation's
	// stream reads them.
	started   chan struct{}
	startOnce sync.Once
	delivered atomic.Int64
	result    atomic.Value // string
}

// Start signals that the relay to the receiver has begun: it closes Started.
func (s *Session) Start() { s.startOnce.Do(func() { close(s.started) }) }

// Started closes when the relay to the receiver begins.
func (s *Session) Started() <-chan struct{} { return s.started }

// AddDelivered adds n to the bytes delivered to the receiver.
func (s *Session) AddDelivered(n int64) { s.delivered.Add(n) }

// Delivered returns the bytes delivered to the receiver.
func (s *Session) Delivered() int64 { return s.delivered.Load() }

// SetResult records the outcome for the sender in the browser. Must be called before Finish.
func (s *Session) SetResult(res string) { s.result.Store(res) }

// Result returns the outcome recorded with SetResult, or "".
func (s *Session) Result() string {
	res, _ := s.result.Load().(string)
	return res
}

// Attached delivers the receiver when a slug is claimed.
func (s *Session) Attached() <-chan Receiver { return s.attached }

// Expired closes if no receiver attaches within the wait timeout.
func (s *Session) Expired() <-chan struct{} { return s.expired }

// Finished closes when the sender has finished (Registry.Finish).
func (s *Session) Finished() <-chan struct{} { return s.finished }

// Aborted closes on the first call to Abort: the sender can interrupt a blocked read instead of
// waiting for the next byte or BASHT_IDLE_TIMEOUT.
func (s *Session) Aborted() <-chan struct{} { return s.aborted }

// Err returns the first error recorded with Abort, or nil.
func (s *Session) Err() error {
	s.reg.mu.Lock()
	defer s.reg.mu.Unlock()
	return s.err
}

// Abort records err only on the first call and closes Aborted.
func (s *Session) Abort(err error) {
	s.reg.mu.Lock()
	defer s.reg.mu.Unlock()
	if s.err == nil {
		s.err = err
		close(s.aborted)
	}
}

// Registry is the only place that mutates session state.
type Registry struct {
	mu          sync.Mutex
	sessions    map[string]*Session
	gone        map[string]string // terminal slug -> reason (Gone*)
	goneQ       []tombstone
	maxGone     int
	maxSessions int
	waitTimeout time.Duration
	gen         func() (string, error)
	// senderGrace, if > 0, keeps a reservation alive for that long after its sender dropped before
	// the receiver: it can reattach with the same token (Detach, BindDetachable).
	senderGrace time.Duration
	// onGraceExpired, if not nil, is called (without the lock) when the grace period expires and closes the link.
	onGraceExpired func(*Session)
}

type tombstone struct {
	slug string
	at   time.Time
}

// NewRegistry creates a registry with a session capacity and a data wait timeout.
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

// SetSenderGrace sets the grace period for browser senders that dropped before the receiver; 0
// disables it. Must be called before serving requests.
func (r *Registry) SetSenderGrace(d time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.senderGrace = d
}

// SenderGrace returns the grace period set with SetSenderGrace.
func (r *Registry) SenderGrace() time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.senderGrace
}

// OnGraceExpired registers the function called when the grace period expires and the link closes.
func (r *Registry) OnGraceExpired(fn func(*Session)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.onGraceExpired = fn
}

// Create opens a session for an upload. length is -1 if unknown.
func (r *Registry) Create(name string, length int64) (*Session, error) {
	return r.create(name, length, "", 0)
}

// Reserve opens a session whose body arrives with a second request: the send from the browser,
// which must show the link before starting the upload. The sender attaches the body with Bind and
// the session's Token within bindTimeout; otherwise the session ends as if the sender had dropped
// (ErrSenderGone), even with a receiver already attached.
func (r *Registry) Reserve(name string, length int64, bindTimeout time.Duration) (*Session, error) {
	token, err := newToken()
	if err != nil {
		return nil, err
	}
	return r.create(name, length, token, bindTimeout)
}

// Bind assigns the reserved session id to the sender. An unknown slug, a wrong token or a
// session already attached return ErrNotFound: whoever lacks the token cannot tell the cases apart.
func (r *Registry) Bind(id, token string) (*Session, error) {
	s, _, err := r.BindDetachable(id, token)
	return s, err
}

// BindDetachable is Bind and also returns the channel that closes when this attachment ends
// without closing the session: the dropped sender (Detach) or an attachment replacing it. With
// the grace period active (SetSenderGrace), a sender that returns with the token reattaches the
// session even if the server has not yet seen the old attachment drop, provided the relay has not
// started and the session has not been interrupted: the old attachment is closed and its handler
// exits without ending the session.
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

// relayStarted reports whether the relay to the receiver has begun (Start).
func (s *Session) relayStarted() bool {
	select {
	case <-s.started:
		return true
	default:
		return false
	}
}

// Away reports whether the body is not attached or the page handling presence is absent.
func (s *Session) Away() bool {
	s.reg.mu.Lock()
	defer s.reg.mu.Unlock()
	return s.awayLocked()
}

func (s *Session) awayLocked() bool {
	return s.Token != "" && (!s.bound || (s.monitored && !s.foreground))
}

// ReceiverPresenceTimeout limits the validity of the receiver page's confirmation.
const ReceiverPresenceTimeout = 5 * time.Second

func (s *Session) peerReadyLocked(now time.Time) bool {
	return s.reg.sessions[s.Slug] == s && s.err == nil && !s.awayLocked() &&
		(s.relayStarted() || (!s.claimed && !s.receiverSeen.IsZero() && now.Sub(s.receiverSeen) < ReceiverPresenceTimeout))
}

// PeerReady confirms the sender and the receiver, without reading or storing the file.
func (s *Session) PeerReady() bool {
	s.reg.mu.Lock()
	defer s.reg.mu.Unlock()
	return s.peerReadyLocked(time.Now())
}

// PeerStatus is an atomic snapshot of the session's connections.
type PeerStatus struct {
	Ready, Away, Claimed, Started bool
}

// ObserveReceiver renews the page's presence without claiming the link.
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

// BeginRelay makes starting the copy and replacing the PUT atomic: an old
// attachment cannot start reading the file after Resume has detached it.
func (r *Registry) BeginRelay(s *Session, binding <-chan struct{}) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if s.Token != "" && (!s.bound || s.unbind != binding) {
		return false
	}
	s.Start()
	return true
}

// Foreground registers the page's availability, without reading the file body.
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

// Resume reopens control of the session. Rebind detaches the old PUT only before
// the relay; during the copy, only the progress stream is recovered.
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

// Requeue puts back the receiver read from Attached by a handler that yields the session to a
// replacing attachment before starting the relay: the new handler will read it.
func (s *Session) Requeue(rcv Receiver) { s.attached <- rcv }

// StreamOpen registers a new progress stream of the sender in the browser (reservation or
// resume). Every StreamOpen must be closed with StreamClosed.
func (r *Registry) StreamOpen(s *Session) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s.streams++
}

// StreamClosed records the closing of the control. For clients without presence, closing the
// last stream before the relay starts the grace period. With presence, the body is independent.
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

// Detach handles the sender dropping from the browser, of the attachment binding (from
// BindDetachable), before the relay starts. With the grace period active the session stays alive
// for senderGrace, without a sender: a receiver that claims it waits, and the sender can reattach
// with the same token (BindDetachable). At expiry the link closes as for a dropped sender. It
// reports true if the session must not be ended by the caller: already yielded to another
// attachment, already in grace or put in grace now. False if the caller must end it (Abort,
// Finish): grace disabled, sender not reserved, relay started, session already finished or interrupted.
func (r *Registry) Detach(s *Session, binding <-chan struct{}) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.detachLocked(s, binding)
}

// detachLocked is Detach with r.mu held.
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

// Reserved returns the reserved session id, attached or not, if token is its token: it serves
// cancellation from the browser. As for Bind, every other case is ErrNotFound.
func (r *Registry) Reserved(id, token string) (*Session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.sessions[id]
	if !ok || s.Token == "" || subtle.ConstantTimeCompare([]byte(s.Token), []byte(token)) != 1 {
		return nil, ErrNotFound
	}
	return s, nil
}

// unbound ignores the callbacks of a replaced attachment; generation 0 is the explicit cancellation.
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

// Cancel ends the reserved session s because the sender in the browser closed the page or
// cancelled: before Bind as at bindTimeout expiry, after with Abort(ErrSenderGone), which
// wakes the sender waiting for the receiver. On a finished session it does nothing.
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

// newLogID generates the session identifier for the logs: 8 random bytes in hex.
func newLogID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// newToken generates the token of a reserved session: 16 random bytes in hex.
func newToken() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// Claim assigns the receiver to the slug. Exactly one claim succeeds per session.
func (r *Registry) Claim(id string, rcv Receiver) (*Session, error) {
	return r.claim(id, rcv, false)
}

// ClaimReady leaves the link intact when the browser must wait for the sender.
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

// Peek reports the state of a slug without claiming it: the session if it waits for a receiver,
// otherwise ErrClaimed, a GoneError (ErrGone) or ErrNotFound as Claim does. The caller reads only
// the immutable fields of the session (name and length for the download page).
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

// Finish ends the session: removes it from the registry once and closes Finished.
// Must be called only from the sender's goroutine, after the last write to the receiver.
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

// Len returns the number of live sessions.
func (r *Registry) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.sessions)
}

// ExpireWaiting immediately expires all sessions not yet claimed. It serves shutdown:
// with no new receivers possible, waiting senders are closed instead of occupying the
// grace period. Transfers in progress are untouched.
func (r *Registry) ExpireWaiting() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, s := range r.sessions {
		r.expireLocked(s)
	}
}

// expire removes a session not claimed at timeout expiry. Idempotent.
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
	// A reservation without a body has no sender that will call Finish.
	if s.Token != "" && !s.bound {
		s.bindTimer.Stop()
		s.finishOnce.Do(func() { close(s.finished) })
	}
}

// finishReason is the reason for the tombstone of a session that ends (r.mu held). A claimed
// slug is always GoneUsed, even if the sender then dropped: the receiver already had the
// link. Without a receiver, expiry is GoneExpired and any other end is a dropped sender.
func finishReason(s *Session) string {
	switch {
	case s.claimed:
		return GoneUsed
	case errors.Is(s.err, ErrExpired):
		return GoneExpired
	}
	return GoneSenderGone
}

// bury records the slug as terminal for tombstoneTTL (D17), within maxGone tombstones.
func (r *Registry) bury(id, reason string) {
	r.gone[id] = reason
	r.goneQ = append(r.goneQ, tombstone{slug: id, at: time.Now()})
	for len(r.goneQ) > r.maxGone {
		r.dropOldest()
	}
}

// dropOldest removes the oldest tombstone and frees the string reference in the array.
func (r *Registry) dropOldest() {
	delete(r.gone, r.goneQ[0].slug)
	r.goneQ[0] = tombstone{}
	r.goneQ = r.goneQ[1:]
}

// goneReason returns the reason of the tombstone for id, if any.
func (r *Registry) goneReason(id string) (string, bool) {
	reason, ok := r.gone[id]
	return reason, ok
}

// prune removes the expired tombstones from the head of the queue (FIFO by date).
func (r *Registry) prune(now time.Time) {
	for len(r.goneQ) > 0 && now.Sub(r.goneQ[0].at) >= tombstoneTTL {
		r.dropOldest()
	}
}
