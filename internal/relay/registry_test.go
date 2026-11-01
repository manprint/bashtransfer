package relay

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeRcv struct{}

func (fakeRcv) Write(p []byte) (int, error) { return len(p), nil }
func (fakeRcv) Flush()                      {}

func TestRegistryClaimOnce(t *testing.T) {
	r := NewRegistry(10, time.Hour)
	s, err := r.Create("a.bin", -1)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = r.Claim(s.Slug, fakeRcv{})
		}(i)
	}
	wg.Wait()
	nils, claimed := 0, 0
	for _, e := range errs {
		switch {
		case e == nil:
			nils++
		case errors.Is(e, ErrClaimed):
			claimed++
		default:
			t.Fatalf("unexpected error: %v", e)
		}
	}
	if nils != 1 || claimed != 1 {
		t.Fatalf("expected 1 successful claim and 1 ErrClaimed, got %d and %d", nils, claimed)
	}
}

func TestRegistryFinishOnce(t *testing.T) {
	r := NewRegistry(10, time.Hour)
	s, err := r.Create("a.bin", -1)
	if err != nil {
		t.Fatal(err)
	}
	r.Finish(s)
	r.Finish(s)
	if r.Len() != 0 {
		t.Fatalf("Len=%d, expected 0", r.Len())
	}
	select {
	case <-s.Finished():
	default:
		t.Fatal("Finished not closed")
	}
	if _, err := r.Claim(s.Slug, fakeRcv{}); !errors.Is(err, ErrGone) {
		t.Fatalf("Claim after Finish: %v, expected ErrGone", err)
	}
}

func TestRegistryWaitTimeout(t *testing.T) {
	r := NewRegistry(10, 50*time.Millisecond)
	s, err := r.Create("a.bin", -1)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-s.Expired():
	case <-time.After(2 * time.Second):
		t.Fatal("Expired not closed within 2 s")
	}
	if r.Len() != 0 {
		t.Fatalf("Len=%d after expiry, expected 0", r.Len())
	}
	if _, err := r.Claim(s.Slug, fakeRcv{}); !errors.Is(err, ErrGone) {
		t.Fatalf("Claim after expiry: %v, expected ErrGone", err)
	}
}

func TestRegistryCapacity(t *testing.T) {
	r := NewRegistry(2, time.Hour)
	a, err := r.Create("a", -1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Create("b", -1); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Create("c", -1); !errors.Is(err, ErrCapacity) {
		t.Fatalf("third session: %v, expected ErrCapacity", err)
	}
	r.Finish(a)
	if _, err := r.Create("c", -1); err != nil {
		t.Fatalf("after Finish: %v", err)
	}
}

func TestRegistrySlugCollision(t *testing.T) {
	r := NewRegistry(10, time.Hour)
	calls := 0
	r.gen = func() (string, error) {
		calls++
		return "SAMESLUG01", nil
	}
	if _, err := r.Create("a", -1); err != nil {
		t.Fatal(err)
	}
	calls = 0
	if _, err := r.Create("b", -1); !errors.Is(err, ErrSlugCollision) {
		t.Fatalf("second Create: %v, expected ErrSlugCollision", err)
	}
	if calls != slugAttempts {
		t.Fatalf("generation attempts %d, expected %d", calls, slugAttempts)
	}
}

func TestRegistryAbortKeepsFirst(t *testing.T) {
	r := NewRegistry(10, time.Hour)
	s, err := r.Create("a", -1)
	if err != nil {
		t.Fatal(err)
	}
	first := errors.New("first")
	s.Abort(first)
	s.Abort(errors.New("second"))
	if got := s.Err(); got != first {
		t.Fatalf("Err=%v, expected the first error", got)
	}
}

// TestRegistryTombstoneCap: beyond maxGone the oldest tombstone is discarded (404 instead of 410)
// and memory stays bounded.
func TestRegistryTombstoneCap(t *testing.T) {
	r := NewRegistry(10, time.Hour)
	r.maxGone = 3
	var slugs []string
	for i := 0; i < 5; i++ {
		s, err := r.Create("a", -1)
		if err != nil {
			t.Fatal(err)
		}
		slugs = append(slugs, s.Slug)
		r.Finish(s)
	}
	if len(r.gone) != 3 || len(r.goneQ) != 3 {
		t.Fatalf("tombstones %d/%d, expected 3", len(r.gone), len(r.goneQ))
	}
	for i, id := range slugs {
		_, err := r.Claim(id, fakeRcv{})
		want := ErrGone
		if i < 2 {
			want = ErrNotFound
		}
		if !errors.Is(err, want) {
			t.Errorf("slug %d: %v, expected %v", i, err, want)
		}
	}
}

// TestRegistryTombstoneExpiry: after tombstoneTTL the slug returns to 404 and the structures empty.
func TestRegistryTombstoneExpiry(t *testing.T) {
	r := NewRegistry(10, time.Hour)
	s, err := r.Create("a", -1)
	if err != nil {
		t.Fatal(err)
	}
	r.Finish(s)
	if _, err := r.Claim(s.Slug, fakeRcv{}); !errors.Is(err, ErrGone) {
		t.Fatalf("before expiry: %v, expected ErrGone", err)
	}
	r.mu.Lock()
	r.goneQ[0].at = time.Now().Add(-tombstoneTTL - time.Second)
	r.mu.Unlock()
	if _, err := r.Claim(s.Slug, fakeRcv{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after expiry: %v, expected ErrNotFound", err)
	}
	if len(r.gone) != 0 || len(r.goneQ) != 0 {
		t.Fatalf("residual tombstones: %d/%d", len(r.gone), len(r.goneQ))
	}
}

// TestRegistryConcurrentLifecycle: many sessions with concurrent Claim, Finish, expiry and Abort.
// Under -race there must be no data race, and at the end no session must be alive.
func TestRegistryConcurrentLifecycle(t *testing.T) {
	r := NewRegistry(1000, 5*time.Millisecond)
	var wg sync.WaitGroup
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s, err := r.Create("a", -1)
			if err != nil {
				t.Error(err)
				return
			}
			var inner sync.WaitGroup
			inner.Add(2)
			go func() {
				defer inner.Done()
				time.Sleep(time.Duration(i%8) * time.Millisecond)
				_, _ = r.Claim(s.Slug, fakeRcv{})
			}()
			go func() {
				defer inner.Done()
				s.Abort(ErrReceiverGone)
				_ = s.Err()
			}()
			select {
			case <-s.Expired():
			case <-time.After(time.Duration(i%8+1) * time.Millisecond):
			}
			r.Finish(s)
			inner.Wait()
			select {
			case <-s.Finished():
			default:
				t.Error("Finished not closed")
			}
		}(i)
	}
	wg.Wait()
	if r.Len() != 0 {
		t.Fatalf("live sessions %d, expected 0", r.Len())
	}
}

// TestRegistryAbortClosesAborted: Aborted closes on the first Abort; subsequent ones do not close
// the channel again and do not change the error.
func TestRegistryAbortClosesAborted(t *testing.T) {
	r := NewRegistry(10, time.Hour)
	s, err := r.Create("a.bin", -1)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-s.Aborted():
		t.Fatal("Aborted closed before Abort")
	default:
	}
	s.Abort(ErrReceiverGone)
	s.Abort(ErrSenderGone)
	select {
	case <-s.Aborted():
	default:
		t.Fatal("Aborted not closed after Abort")
	}
	if !errors.Is(s.Err(), ErrReceiverGone) {
		t.Fatalf("Err %v, expected the first error", s.Err())
	}
}

// TestRegistryPeek: Peek reports the state without claiming the slug.
func TestRegistryPeek(t *testing.T) {
	r := NewRegistry(10, time.Hour)
	s, err := r.Create("a.bin", -1)
	if err != nil {
		t.Fatal(err)
	}
	if p, err := r.Peek(s.Slug); err != nil || p != s {
		t.Fatalf("Peek slug waiting: %v, %v", p, err)
	}
	if _, err := r.Peek(s.Slug); err != nil {
		t.Fatalf("repeated Peek: %v, the slug must not appear claimed", err)
	}
	if _, err := r.Peek("ZZZZZZZZZZ"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Peek unknown: %v", err)
	}
	if _, err := r.Claim(s.Slug, fakeRcv{}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Peek(s.Slug); !errors.Is(err, ErrClaimed) {
		t.Fatalf("Peek claimed: %v", err)
	}
	r.Finish(s)
	if _, err := r.Peek(s.Slug); !errors.Is(err, ErrGone) {
		t.Fatalf("Peek ended: %v", err)
	}
}

// TestRegistryReserve: a reserved session attaches only once and only with its token.
func TestRegistryReserve(t *testing.T) {
	r := NewRegistry(10, time.Hour)
	s, err := r.Reserve("a.bin", 3, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Token) != 32 {
		t.Fatalf("token %q", s.Token)
	}
	if p, err := r.Peek(s.Slug); err != nil || p != s {
		t.Fatalf("Peek reserved: %v, %v", p, err)
	}
	for _, tok := range []string{"", "x", strings.Repeat("0", 32)} {
		if _, err := r.Bind(s.Slug, tok); !errors.Is(err, ErrNotFound) {
			t.Fatalf("Bind token %q: %v", tok, err)
		}
	}
	if _, err := r.Bind("ZZZZZZZZZZ", s.Token); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Bind unknown slug: %v", err)
	}
	if b, err := r.Bind(s.Slug, s.Token); err != nil || b != s {
		t.Fatalf("Bind: %v, %v", b, err)
	}
	if _, err := r.Bind(s.Slug, s.Token); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second Bind: %v", err)
	}
	c, err := r.Create("b.bin", -1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Bind(c.Slug, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Bind of a Create session: %v", err)
	}
}

// TestRegistryReserveUnbound: without Bind within the timeout the session ends with ErrSenderGone,
// even if a receiver has already attached; the slug becomes 410.
func TestRegistryReserveUnbound(t *testing.T) {
	r := NewRegistry(10, time.Hour)
	s, err := r.Reserve("a.bin", 3, 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Claim(s.Slug, fakeRcv{}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-s.Finished():
	case <-time.After(5 * time.Second):
		t.Fatal("session without sender not ended")
	}
	if !errors.Is(s.Err(), ErrSenderGone) {
		t.Fatalf("Err %v, expected ErrSenderGone", s.Err())
	}
	if _, err := r.Peek(s.Slug); !errors.Is(err, ErrGone) || r.Len() != 0 {
		t.Fatalf("Peek %v, sessions %d", err, r.Len())
	}
	if _, err := r.Bind(s.Slug, s.Token); !errors.Is(err, ErrNotFound) {
		t.Fatalf("late Bind: %v", err)
	}

	// Attached in time: the timeout does not touch it.
	s2, _ := r.Reserve("b.bin", 3, 50*time.Millisecond)
	if _, err := r.Bind(s2.Slug, s2.Token); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	if _, err := r.Peek(s2.Slug); err != nil {
		t.Fatalf("attached session: %v", err)
	}
}

// TestRegistryCancel: before Bind, Cancel ends the reservation like bindTimeout; after, it
// interrupts with ErrSenderGone and leaves it to the sender, which calls Finish. On a finished session
// it does nothing.
func TestRegistryCancel(t *testing.T) {
	r := NewRegistry(10, time.Hour)
	s, _ := r.Reserve("a.bin", 3, time.Hour)
	r.Cancel(s)
	select {
	case <-s.Finished():
	default:
		t.Fatal("cancelled reservation not ended")
	}
	if !errors.Is(s.Err(), ErrSenderGone) || r.Len() != 0 {
		t.Fatalf("Err %v, sessions %d", s.Err(), r.Len())
	}
	if _, err := r.Claim(s.Slug, fakeRcv{}); !errors.Is(err, ErrGone) {
		t.Fatalf("Claim after Cancel: %v", err)
	}
	if _, err := r.Bind(s.Slug, s.Token); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Bind after Cancel: %v", err)
	}

	s2, _ := r.Reserve("b.bin", 3, time.Hour)
	if _, err := r.Bind(s2.Slug, s2.Token); err != nil {
		t.Fatal(err)
	}
	r.Cancel(s2)
	select {
	case <-s2.Aborted():
	default:
		t.Fatal("attached session not interrupted")
	}
	select {
	case <-s2.Finished():
		t.Fatal("attached session ended without Finish")
	default:
	}
	if !errors.Is(s2.Err(), ErrSenderGone) {
		t.Fatalf("Err %v", s2.Err())
	}
	r.Finish(s2)
	r.Cancel(s2)
	if r.Len() != 0 {
		t.Fatalf("sessions %d", r.Len())
	}
}

// TestRegistryReserved: the reservation is found only with its token, attached or not; an upload
// from curl (without a token) never.
func TestRegistryReserved(t *testing.T) {
	r := NewRegistry(10, time.Hour)
	s, _ := r.Reserve("a.bin", 3, time.Hour)
	for _, tok := range []string{"", "x", strings.Repeat("0", 32)} {
		if _, err := r.Reserved(s.Slug, tok); !errors.Is(err, ErrNotFound) {
			t.Fatalf("Reserved token %q: %v", tok, err)
		}
	}
	if got, err := r.Reserved(s.Slug, s.Token); err != nil || got != s {
		t.Fatalf("Reserved: %v, %v", got, err)
	}
	if _, err := r.Bind(s.Slug, s.Token); err != nil {
		t.Fatal(err)
	}
	if got, err := r.Reserved(s.Slug, s.Token); err != nil || got != s {
		t.Fatalf("Reserved attached: %v, %v", got, err)
	}
	c, _ := r.Create("b.bin", 3)
	if _, err := r.Reserved(c.Slug, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Reserved on curl upload: %v", err)
	}
	r.Finish(s)
	if _, err := r.Reserved(s.Slug, s.Token); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Reserved after Finish: %v", err)
	}
}

// TestRegistryExpireReservation: a reservation without a body expires like an upload and, without a
// sender calling Finish, ends; if attached, the sender ends it.
func TestRegistryExpireReservation(t *testing.T) {
	r := NewRegistry(10, 50*time.Millisecond)
	s, _ := r.Reserve("a.bin", 3, time.Hour)
	select {
	case <-s.Finished():
	case <-time.After(5 * time.Second):
		t.Fatal("expired reservation not ended")
	}
	select {
	case <-s.Expired():
	default:
		t.Fatal("Expired not closed")
	}
	s2, _ := r.Reserve("b.bin", 3, time.Hour)
	if _, err := r.Bind(s2.Slug, s2.Token); err != nil {
		t.Fatal(err)
	}
	<-s2.Expired()
	select {
	case <-s2.Finished():
		t.Fatal("attached session ended at expiry without Finish")
	default:
	}
}

// TestSessionProgress: progress and outcome for the reservation's stream.
func TestSessionProgress(t *testing.T) {
	r := NewRegistry(10, time.Hour)
	s, _ := r.Reserve("a.bin", 3, time.Hour)
	select {
	case <-s.Started():
		t.Fatal("Started closed before Start")
	default:
	}
	s.Start()
	s.Start()
	<-s.Started()
	s.AddDelivered(2)
	s.AddDelivered(1)
	if s.Delivered() != 3 || s.Result() != "" {
		t.Fatalf("Delivered %d, Result %q", s.Delivered(), s.Result())
	}
	s.SetResult("ok")
	if s.Result() != "ok" {
		t.Fatalf("Result %q", s.Result())
	}
}

// TestRegistryExpireWaiting: waiting sessions expire at once (Expired closed, slug 410);
// claimed ones remain.
func TestRegistryExpireWaiting(t *testing.T) {
	r := NewRegistry(10, time.Hour)
	waiting, err := r.Create("a.bin", -1)
	if err != nil {
		t.Fatal(err)
	}
	active, err := r.Create("b.bin", -1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Claim(active.Slug, fakeRcv{}); err != nil {
		t.Fatal(err)
	}
	r.ExpireWaiting()
	r.ExpireWaiting()
	select {
	case <-waiting.Expired():
	default:
		t.Fatal("waiting session not expired")
	}
	select {
	case <-active.Expired():
		t.Fatal("claimed session expired")
	default:
	}
	if r.Len() != 1 {
		t.Fatalf("live sessions %d, expected 1", r.Len())
	}
	if _, err := r.Claim(waiting.Slug, fakeRcv{}); !errors.Is(err, ErrGone) {
		t.Fatalf("Claim after ExpireWaiting: %v, expected ErrGone", err)
	}
}

// TestRegistryGoneReason: the tombstone remembers why the slug is terminal; every GoneError is also
// ErrGone.
func TestRegistryGoneReason(t *testing.T) {
	r := NewRegistry(10, time.Hour)
	reason := func(id string) string {
		t.Helper()
		_, err := r.Peek(id)
		var g *GoneError
		if !errors.Is(err, ErrGone) || !errors.As(err, &g) {
			t.Fatalf("Peek: %v, expected GoneError", err)
		}
		if _, err := r.Claim(id, fakeRcv{}); !errors.Is(err, ErrGone) {
			t.Fatalf("Claim: %v, expected ErrGone", err)
		}
		return g.Reason
	}

	sent, _ := r.Create("a", -1)
	sent.Abort(ErrSenderGone)
	r.Finish(sent)
	if got := reason(sent.Slug); got != GoneSenderGone {
		t.Errorf("sender dropped without receiver: %q", got)
	}

	used, _ := r.Create("b", -1)
	if _, err := r.Claim(used.Slug, fakeRcv{}); err != nil {
		t.Fatal(err)
	}
	r.Finish(used)
	if got := reason(used.Slug); got != GoneUsed {
		t.Errorf("claimed and concluded: %q", got)
	}

	cut, _ := r.Create("c", -1)
	if _, err := r.Claim(cut.Slug, fakeRcv{}); err != nil {
		t.Fatal(err)
	}
	cut.Abort(ErrSenderGone)
	r.Finish(cut)
	if got := reason(cut.Slug); got != GoneUsed {
		t.Errorf("claimed, then sender dropped: %q", got)
	}

	late, _ := r.Create("d", -1)
	r.ExpireWaiting()
	if got := reason(late.Slug); got != GoneExpired {
		t.Errorf("expired: %q", got)
	}

	res, _ := r.Reserve("e", 1, time.Hour)
	r.Cancel(res)
	if got := reason(res.Slug); got != GoneSenderGone {
		t.Errorf("reservation cancelled before body: %q", got)
	}
}

// reservedBound reserves a session and attaches its body.
func reservedBound(t *testing.T, r *Registry) (*Session, <-chan struct{}) {
	t.Helper()
	s, err := r.Reserve("a.bin", 10, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	_, binding, err := r.BindDetachable(s.Slug, s.Token)
	if err != nil {
		t.Fatal(err)
	}
	return s, binding
}

// TestRegistryDetachDisabled: without grace Detach does nothing and the caller ends the
// session; a second attachment with the old one still present is rejected, as always.
func TestRegistryDetachDisabled(t *testing.T) {
	r := NewRegistry(10, time.Hour)
	s, binding := reservedBound(t, r)
	if r.Detach(s, binding) {
		t.Fatal("Detach without grace must report false")
	}
	if _, err := r.Bind(s.Slug, s.Token); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second Bind: %v, expected ErrNotFound", err)
	}
}

// TestRegistryDetachRebind: the dropped sender stays in grace, a receiver can claim the
// link and the sender that returns with the token reattaches the session.
func TestRegistryDetachRebind(t *testing.T) {
	r := NewRegistry(10, time.Hour)
	r.SetSenderGrace(time.Hour)
	s, binding := reservedBound(t, r)
	if !r.Detach(s, binding) {
		t.Fatal("Detach with grace must report true")
	}
	select {
	case <-binding:
	default:
		t.Fatal("attachment channel not closed after Detach")
	}
	if !s.Away() || r.Len() != 1 {
		t.Fatalf("Away=%v, sessions %d: expected a live session without a sender", s.Away(), r.Len())
	}
	if !r.Detach(s, binding) {
		t.Fatal("second Detach: expected true (already in grace)")
	}
	if _, err := r.Peek(s.Slug); err != nil {
		t.Fatalf("Peek in grace: %v", err)
	}
	// The receiver claims while the sender is absent: it waits in the channel.
	if _, err := r.Claim(s.Slug, fakeRcv{}); err != nil {
		t.Fatalf("Claim in grace: %v", err)
	}
	if _, _, err := r.BindDetachable(s.Slug, strings.Repeat("0", 32)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Bind with wrong token: %v", err)
	}
	s2, binding2, err := r.BindDetachable(s.Slug, s.Token)
	if err != nil || s2 != s {
		t.Fatalf("reattach: %v", err)
	}
	if s.Away() {
		t.Fatal("Away after reattach")
	}
	select {
	case rcv := <-s.Attached():
		if rcv == nil {
			t.Fatal("null receiver")
		}
	default:
		t.Fatal("the receiver claimed in grace is not queued for the new attachment")
	}
	// A Detach of the old attachment does not touch the new one.
	if !r.Detach(s, binding) || s.Away() {
		t.Fatal("Detach of a replaced attachment removed the new sender")
	}
	select {
	case <-binding2:
		t.Fatal("the new attachment was closed by the old one's Detach")
	default:
	}
}

// TestRegistryBindTakeover: the sender that returns before the server sees the old
// attachment drop replaces it; not once the relay has started.
func TestRegistryBindTakeover(t *testing.T) {
	r := NewRegistry(10, time.Hour)
	r.SetSenderGrace(time.Hour)
	s, old := reservedBound(t, r)
	_, fresh, err := r.BindDetachable(s.Slug, s.Token)
	if err != nil {
		t.Fatalf("replacing Bind: %v", err)
	}
	select {
	case <-old:
	default:
		t.Fatal("the old attachment was not closed")
	}
	s.Start()
	if _, _, err := r.BindDetachable(s.Slug, s.Token); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Bind after relay started: %v, expected ErrNotFound", err)
	}
	if r.Detach(s, fresh) {
		t.Fatal("Detach after relay started must report false")
	}
}

// TestRegistryGraceExpires: once grace expires the link closes as for a dropped sender and the
// registered function is called; a waiting receiver sees the session ended.
func TestRegistryGraceExpires(t *testing.T) {
	r := NewRegistry(10, time.Hour)
	r.SetSenderGrace(100 * time.Millisecond)
	expired := make(chan *Session, 1)
	r.OnGraceExpired(func(s *Session) { expired <- s })
	s, binding := reservedBound(t, r)
	if _, err := r.Claim(s.Slug, fakeRcv{}); err != nil {
		t.Fatal(err)
	}
	if !r.Detach(s, binding) {
		t.Fatal("Detach: expected true")
	}
	select {
	case <-expired:
	case <-time.After(3 * time.Second):
		t.Fatal("grace did not expire within 3 s")
	}
	select {
	case <-s.Finished():
	default:
		t.Fatal("Finished not closed at grace expiry")
	}
	if !errors.Is(s.Err(), ErrSenderGone) || r.Len() != 0 {
		t.Fatalf("Err %v, sessions %d", s.Err(), r.Len())
	}
	if _, err := r.Peek(s.Slug); !errors.Is(err, ErrGone) {
		t.Fatalf("Peek after expiry: %v", err)
	}
}

// TestRegistryGraceRebindStopsTimer: rebinding stops the grace expiry timer.
func TestRegistryGraceRebindStopsTimer(t *testing.T) {
	r := NewRegistry(10, time.Hour)
	r.SetSenderGrace(150 * time.Millisecond)
	s, binding := reservedBound(t, r)
	r.Detach(s, binding)
	if _, _, err := r.BindDetachable(s.Slug, s.Token); err != nil {
		t.Fatal(err)
	}
	time.Sleep(400 * time.Millisecond)
	if r.Len() != 1 || s.Err() != nil {
		t.Fatalf("sessions %d, Err %v: rebinding did not stop the expiry", r.Len(), s.Err())
	}
}

func TestRegistryStaleGraceTimerDoesNotExpireNewDetach(t *testing.T) {
	r := NewRegistry(10, time.Hour)
	r.SetSenderGrace(time.Minute)
	s, old := reservedBound(t, r)
	t.Cleanup(func() { r.Cancel(s); r.Finish(s) })
	expired := make(chan *Session, 1)
	r.OnGraceExpired(func(s *Session) { expired <- s })
	r.Detach(s, old)
	r.mu.Lock()
	stale := s.bindTimer
	r.mu.Unlock()
	_, fresh, err := r.BindDetachable(s.Slug, s.Token)
	if err != nil || !r.Detach(s, fresh) {
		t.Fatalf("second disconnect: %v", err)
	}
	// The old timer's callback was already queued when the sender rebound.
	stale.Reset(0)
	select {
	case <-expired:
		t.Fatal("the old timer expired the new grace period")
	case <-time.After(100 * time.Millisecond):
	}
	if _, err := r.Peek(s.Slug); err != nil {
		t.Fatalf("new grace period interrupted by the previous timer: %v", err)
	}
}

func TestRegistryForegroundLifecycle(t *testing.T) {
	r := NewRegistry(10, time.Hour)
	r.SetSenderGrace(time.Minute)
	s, old := reservedBound(t, r)
	t.Cleanup(func() { r.Cancel(s); r.Finish(s) })
	if err := r.Foreground(s.Slug, s.Token, true); err != nil {
		t.Fatal(err)
	}
	r.StreamOpen(s)
	if r.StreamClosed(s) || s.Away() {
		t.Fatal("closing the control alone detached the body")
	}
	if err := r.Foreground(s.Slug, "wrong", false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("presence with wrong token: %v", err)
	}
	if err := r.Foreground(s.Slug, s.Token, false); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ClaimReady(s.Slug, fakeRcv{}); !errors.Is(err, ErrSenderAway) {
		t.Fatalf("browser download with sender absent: %v", err)
	}
	if _, err := r.Peek(s.Slug); err != nil {
		t.Fatalf("pending download consumed the link: %v", err)
	}
	if _, started, err := r.Resume(s.Slug, s.Token, true); err != nil || started {
		t.Fatalf("resume before relay: started=%v err=%v", started, err)
	}
	if r.BeginRelay(s, old) {
		t.Fatal("the old PUT started the relay after resume")
	}
	_, fresh, err := r.BindDetachable(s.Slug, s.Token)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.ClaimReady(s.Slug, fakeRcv{}); err != nil || !r.BeginRelay(s, fresh) {
		t.Fatalf("start from the new PUT: %v", err)
	}
	if _, started, err := r.Resume(s.Slug, s.Token, true); err != nil || !started {
		t.Fatalf("resume during relay: started=%v err=%v", started, err)
	}
	select {
	case <-fresh:
		t.Fatal("control recovery closed the body being transferred")
	default:
	}
}

func TestRegistryForegroundHeartbeatExpires(t *testing.T) {
	r := NewRegistry(10, time.Hour)
	r.SetSenderGrace(time.Minute)
	s, _ := reservedBound(t, r)
	t.Cleanup(func() { r.Cancel(s); r.Finish(s) })
	if err := r.Foreground(s.Slug, s.Token, true); err != nil {
		t.Fatal(err)
	}
	// Simulates presence expiry without waiting for the production timeout.
	r.mu.Lock()
	s.presenceTimer.Reset(20 * time.Millisecond)
	r.mu.Unlock()
	for deadline := time.Now().Add(time.Second); !s.Away(); time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("presence expired without putting the sender in grace")
		}
	}
	if _, err := r.Peek(s.Slug); err != nil {
		t.Fatalf("presence expiry closed the link instead of entering grace: %v", err)
	}
}

func TestRegistryPeerReadinessLifecycle(t *testing.T) {
	r := NewRegistry(10, time.Hour)
	r.SetSenderGrace(time.Minute)
	s, binding := reservedBound(t, r)
	t.Cleanup(func() { r.Cancel(s); r.Finish(s) })
	if _, err := r.Peek(s.Slug); err != nil || s.PeerReady() {
		t.Fatalf("preview alone must not confirm a receiver: %v", err)
	}
	observe := func(want bool) {
		t.Helper()
		peer, err := r.ObserveReceiver(s.Slug)
		if err != nil || peer.Ready != want || s.PeerReady() != want {
			t.Fatalf("receiver confirmation: %+v, err=%v, expected ready=%v", peer, err, want)
		}
	}
	observe(true)
	if _, err := r.Peek(s.Slug); err != nil {
		t.Fatalf("confirmation consumed the link: %v", err)
	}
	r.mu.Lock()
	s.receiverSeen = time.Now().Add(-ReceiverPresenceTimeout)
	r.mu.Unlock()
	if s.PeerReady() {
		t.Fatal("receiver no longer confirmed but still READY")
	}
	if err := r.Foreground(s.Slug, s.Token, false); err != nil {
		t.Fatal(err)
	}
	observe(false)
	if _, _, err := r.Resume(s.Slug, s.Token, true); err != nil {
		t.Fatal(err)
	}
	observe(false)
	var err error
	_, binding, err = r.BindDetachable(s.Slug, s.Token)
	if err != nil {
		t.Fatal(err)
	}
	observe(true)
	if _, err := r.ClaimReady(s.Slug, fakeRcv{}); err != nil {
		t.Fatal(err)
	}
	observe(false)
	if !r.BeginRelay(s, binding) {
		t.Fatal("relay not started")
	}
	r.mu.Lock()
	s.receiverSeen = time.Now().Add(-2 * ReceiverPresenceTimeout)
	r.mu.Unlock()
	if !s.PeerReady() {
		t.Fatal("the active data connection does not confirm the receiver")
	}
	if err := r.Foreground(s.Slug, s.Token, false); err != nil || s.PeerReady() {
		t.Fatalf("sender in background during relay still READY: %v", err)
	}
	if err := r.Foreground(s.Slug, s.Token, true); err != nil || !s.PeerReady() {
		t.Fatalf("return during relay not confirmed: %v", err)
	}
	r.Cancel(s)
	if s.PeerReady() {
		t.Fatal("cancelled session still READY")
	}
	r.Finish(s)
	if _, err := r.ObserveReceiver(s.Slug); !errors.Is(err, ErrGone) || s.PeerReady() {
		t.Fatalf("finished session still available: %v", err)
	}
}
