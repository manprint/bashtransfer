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
			t.Fatalf("errore inatteso: %v", e)
		}
	}
	if nils != 1 || claimed != 1 {
		t.Fatalf("attesi 1 claim riuscito e 1 ErrClaimed, ottenuti %d e %d", nils, claimed)
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
		t.Fatalf("Len=%d, atteso 0", r.Len())
	}
	select {
	case <-s.Finished():
	default:
		t.Fatal("Finished non chiuso")
	}
	if _, err := r.Claim(s.Slug, fakeRcv{}); !errors.Is(err, ErrGone) {
		t.Fatalf("Claim dopo Finish: %v, atteso ErrGone", err)
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
		t.Fatal("Expired non chiuso entro 2 s")
	}
	if r.Len() != 0 {
		t.Fatalf("Len=%d dopo scadenza, atteso 0", r.Len())
	}
	if _, err := r.Claim(s.Slug, fakeRcv{}); !errors.Is(err, ErrGone) {
		t.Fatalf("Claim dopo scadenza: %v, atteso ErrGone", err)
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
		t.Fatalf("terza sessione: %v, atteso ErrCapacity", err)
	}
	r.Finish(a)
	if _, err := r.Create("c", -1); err != nil {
		t.Fatalf("dopo Finish: %v", err)
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
		t.Fatalf("secondo Create: %v, atteso ErrSlugCollision", err)
	}
	if calls != slugAttempts {
		t.Fatalf("tentativi generazione %d, attesi %d", calls, slugAttempts)
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
		t.Fatalf("Err=%v, atteso il primo errore", got)
	}
}

// TestRegistryTombstoneCap: oltre maxGone la tombstone più vecchia viene scartata (404 invece di 410)
// e la memoria resta limitata.
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
		t.Fatalf("tombstone %d/%d, attese 3", len(r.gone), len(r.goneQ))
	}
	for i, id := range slugs {
		_, err := r.Claim(id, fakeRcv{})
		want := ErrGone
		if i < 2 {
			want = ErrNotFound
		}
		if !errors.Is(err, want) {
			t.Errorf("slug %d: %v, atteso %v", i, err, want)
		}
	}
}

// TestRegistryTombstoneExpiry: dopo tombstoneTTL lo slug torna 404 e le strutture si svuotano.
func TestRegistryTombstoneExpiry(t *testing.T) {
	r := NewRegistry(10, time.Hour)
	s, err := r.Create("a", -1)
	if err != nil {
		t.Fatal(err)
	}
	r.Finish(s)
	if _, err := r.Claim(s.Slug, fakeRcv{}); !errors.Is(err, ErrGone) {
		t.Fatalf("prima della scadenza: %v, atteso ErrGone", err)
	}
	r.mu.Lock()
	r.goneQ[0].at = time.Now().Add(-tombstoneTTL - time.Second)
	r.mu.Unlock()
	if _, err := r.Claim(s.Slug, fakeRcv{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("dopo la scadenza: %v, atteso ErrNotFound", err)
	}
	if len(r.gone) != 0 || len(r.goneQ) != 0 {
		t.Fatalf("tombstone residue: %d/%d", len(r.gone), len(r.goneQ))
	}
}

// TestRegistryConcurrentLifecycle: molte sessioni con Claim, Finish, scadenza e Abort concorrenti.
// Sotto -race non devono esserci data race e a fine corsa nessuna sessione viva.
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
				t.Error("Finished non chiuso")
			}
		}(i)
	}
	wg.Wait()
	if r.Len() != 0 {
		t.Fatalf("sessioni vive %d, attese 0", r.Len())
	}
}

// TestRegistryAbortClosesAborted: Aborted si chiude alla prima Abort; le successive non chiudono
// di nuovo il canale e non cambiano l'errore.
func TestRegistryAbortClosesAborted(t *testing.T) {
	r := NewRegistry(10, time.Hour)
	s, err := r.Create("a.bin", -1)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-s.Aborted():
		t.Fatal("Aborted chiuso prima di Abort")
	default:
	}
	s.Abort(ErrReceiverGone)
	s.Abort(ErrSenderGone)
	select {
	case <-s.Aborted():
	default:
		t.Fatal("Aborted non chiuso dopo Abort")
	}
	if !errors.Is(s.Err(), ErrReceiverGone) {
		t.Fatalf("Err %v, atteso il primo errore", s.Err())
	}
}

// TestRegistryPeek: Peek riporta lo stato senza reclamare lo slug.
func TestRegistryPeek(t *testing.T) {
	r := NewRegistry(10, time.Hour)
	s, err := r.Create("a.bin", -1)
	if err != nil {
		t.Fatal(err)
	}
	if p, err := r.Peek(s.Slug); err != nil || p != s {
		t.Fatalf("Peek slug in attesa: %v, %v", p, err)
	}
	if _, err := r.Peek(s.Slug); err != nil {
		t.Fatalf("Peek ripetuto: %v, lo slug non deve risultare reclamato", err)
	}
	if _, err := r.Peek("ZZZZZZZZZZ"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Peek sconosciuto: %v", err)
	}
	if _, err := r.Claim(s.Slug, fakeRcv{}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Peek(s.Slug); !errors.Is(err, ErrClaimed) {
		t.Fatalf("Peek reclamato: %v", err)
	}
	r.Finish(s)
	if _, err := r.Peek(s.Slug); !errors.Is(err, ErrGone) {
		t.Fatalf("Peek concluso: %v", err)
	}
}

// TestRegistryReserve: una sessione prenotata si aggancia una sola volta e solo con il suo token.
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
		t.Fatalf("Peek prenotata: %v, %v", p, err)
	}
	for _, tok := range []string{"", "x", strings.Repeat("0", 32)} {
		if _, err := r.Bind(s.Slug, tok); !errors.Is(err, ErrNotFound) {
			t.Fatalf("Bind token %q: %v", tok, err)
		}
	}
	if _, err := r.Bind("ZZZZZZZZZZ", s.Token); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Bind slug sconosciuto: %v", err)
	}
	if b, err := r.Bind(s.Slug, s.Token); err != nil || b != s {
		t.Fatalf("Bind: %v, %v", b, err)
	}
	if _, err := r.Bind(s.Slug, s.Token); !errors.Is(err, ErrNotFound) {
		t.Fatalf("secondo Bind: %v", err)
	}
	c, err := r.Create("b.bin", -1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Bind(c.Slug, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Bind di una sessione di Create: %v", err)
	}
}

// TestRegistryReserveUnbound: senza Bind entro il timeout la sessione termina con ErrSenderGone,
// anche se un ricevente si è già agganciato; lo slug diventa 410.
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
		t.Fatal("sessione senza mittente non terminata")
	}
	if !errors.Is(s.Err(), ErrSenderGone) {
		t.Fatalf("Err %v, atteso ErrSenderGone", s.Err())
	}
	if _, err := r.Peek(s.Slug); !errors.Is(err, ErrGone) || r.Len() != 0 {
		t.Fatalf("Peek %v, sessioni %d", err, r.Len())
	}
	if _, err := r.Bind(s.Slug, s.Token); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Bind tardivo: %v", err)
	}

	// Agganciata in tempo: il timeout non la tocca.
	s2, _ := r.Reserve("b.bin", 3, 50*time.Millisecond)
	if _, err := r.Bind(s2.Slug, s2.Token); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	if _, err := r.Peek(s2.Slug); err != nil {
		t.Fatalf("sessione agganciata: %v", err)
	}
}

// TestRegistryCancel: prima del Bind Cancel termina la prenotazione come bindTimeout; dopo la
// interrompe con ErrSenderGone e la lascia al mittente, che chiama Finish. Su una sessione finita
// non fa nulla.
func TestRegistryCancel(t *testing.T) {
	r := NewRegistry(10, time.Hour)
	s, _ := r.Reserve("a.bin", 3, time.Hour)
	r.Cancel(s)
	select {
	case <-s.Finished():
	default:
		t.Fatal("prenotazione annullata non terminata")
	}
	if !errors.Is(s.Err(), ErrSenderGone) || r.Len() != 0 {
		t.Fatalf("Err %v, sessioni %d", s.Err(), r.Len())
	}
	if _, err := r.Claim(s.Slug, fakeRcv{}); !errors.Is(err, ErrGone) {
		t.Fatalf("Claim dopo Cancel: %v", err)
	}
	if _, err := r.Bind(s.Slug, s.Token); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Bind dopo Cancel: %v", err)
	}

	s2, _ := r.Reserve("b.bin", 3, time.Hour)
	if _, err := r.Bind(s2.Slug, s2.Token); err != nil {
		t.Fatal(err)
	}
	r.Cancel(s2)
	select {
	case <-s2.Aborted():
	default:
		t.Fatal("sessione agganciata non interrotta")
	}
	select {
	case <-s2.Finished():
		t.Fatal("sessione agganciata terminata senza Finish")
	default:
	}
	if !errors.Is(s2.Err(), ErrSenderGone) {
		t.Fatalf("Err %v", s2.Err())
	}
	r.Finish(s2)
	r.Cancel(s2)
	if r.Len() != 0 {
		t.Fatalf("sessioni %d", r.Len())
	}
}

// TestRegistryReserved: la prenotazione si trova solo con il suo token, agganciata o no; un upload
// da curl (senza token) mai.
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
		t.Fatalf("Reserved agganciata: %v, %v", got, err)
	}
	c, _ := r.Create("b.bin", 3)
	if _, err := r.Reserved(c.Slug, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Reserved su upload da curl: %v", err)
	}
	r.Finish(s)
	if _, err := r.Reserved(s.Slug, s.Token); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Reserved dopo Finish: %v", err)
	}
}

// TestRegistryExpireReservation: una prenotazione senza body scade come un upload e, senza un
// mittente che chiami Finish, termina; agganciata la termina il mittente.
func TestRegistryExpireReservation(t *testing.T) {
	r := NewRegistry(10, 50*time.Millisecond)
	s, _ := r.Reserve("a.bin", 3, time.Hour)
	select {
	case <-s.Finished():
	case <-time.After(5 * time.Second):
		t.Fatal("prenotazione scaduta non terminata")
	}
	select {
	case <-s.Expired():
	default:
		t.Fatal("Expired non chiuso")
	}
	s2, _ := r.Reserve("b.bin", 3, time.Hour)
	if _, err := r.Bind(s2.Slug, s2.Token); err != nil {
		t.Fatal(err)
	}
	<-s2.Expired()
	select {
	case <-s2.Finished():
		t.Fatal("sessione agganciata terminata alla scadenza senza Finish")
	default:
	}
}

// TestSessionProgress: avanzamento ed esito per lo stream della prenotazione.
func TestSessionProgress(t *testing.T) {
	r := NewRegistry(10, time.Hour)
	s, _ := r.Reserve("a.bin", 3, time.Hour)
	select {
	case <-s.Started():
		t.Fatal("Started chiuso prima di Start")
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

// TestRegistryExpireWaiting: le sessioni in attesa scadono subito (Expired chiuso, slug 410);
// quelle reclamate restano.
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
		t.Fatal("sessione in attesa non scaduta")
	}
	select {
	case <-active.Expired():
		t.Fatal("sessione reclamata scaduta")
	default:
	}
	if r.Len() != 1 {
		t.Fatalf("sessioni vive %d, attesa 1", r.Len())
	}
	if _, err := r.Claim(waiting.Slug, fakeRcv{}); !errors.Is(err, ErrGone) {
		t.Fatalf("Claim dopo ExpireWaiting: %v, atteso ErrGone", err)
	}
}

// TestRegistryGoneReason: la tombstone ricorda perché lo slug è terminale; ogni GoneError è anche
// ErrGone.
func TestRegistryGoneReason(t *testing.T) {
	r := NewRegistry(10, time.Hour)
	reason := func(id string) string {
		t.Helper()
		_, err := r.Peek(id)
		var g *GoneError
		if !errors.Is(err, ErrGone) || !errors.As(err, &g) {
			t.Fatalf("Peek: %v, atteso GoneError", err)
		}
		if _, err := r.Claim(id, fakeRcv{}); !errors.Is(err, ErrGone) {
			t.Fatalf("Claim: %v, atteso ErrGone", err)
		}
		return g.Reason
	}

	sent, _ := r.Create("a", -1)
	sent.Abort(ErrSenderGone)
	r.Finish(sent)
	if got := reason(sent.Slug); got != GoneSenderGone {
		t.Errorf("mittente caduto senza ricevente: %q", got)
	}

	used, _ := r.Create("b", -1)
	if _, err := r.Claim(used.Slug, fakeRcv{}); err != nil {
		t.Fatal(err)
	}
	r.Finish(used)
	if got := reason(used.Slug); got != GoneUsed {
		t.Errorf("reclamato e concluso: %q", got)
	}

	cut, _ := r.Create("c", -1)
	if _, err := r.Claim(cut.Slug, fakeRcv{}); err != nil {
		t.Fatal(err)
	}
	cut.Abort(ErrSenderGone)
	r.Finish(cut)
	if got := reason(cut.Slug); got != GoneUsed {
		t.Errorf("reclamato, poi mittente caduto: %q", got)
	}

	late, _ := r.Create("d", -1)
	r.ExpireWaiting()
	if got := reason(late.Slug); got != GoneExpired {
		t.Errorf("scaduto: %q", got)
	}

	res, _ := r.Reserve("e", 1, time.Hour)
	r.Cancel(res)
	if got := reason(res.Slug); got != GoneSenderGone {
		t.Errorf("prenotazione annullata prima del body: %q", got)
	}
}

// reservedBound prenota una sessione e ne aggancia il body.
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

// TestRegistryDetachDisabled: senza tolleranza Detach non fa nulla e il chiamante termina la
// sessione; un secondo aggancio con il vecchio ancora presente è rifiutato, come sempre.
func TestRegistryDetachDisabled(t *testing.T) {
	r := NewRegistry(10, time.Hour)
	s, binding := reservedBound(t, r)
	if r.Detach(s, binding) {
		t.Fatal("Detach senza tolleranza deve riportare false")
	}
	if _, err := r.Bind(s.Slug, s.Token); !errors.Is(err, ErrNotFound) {
		t.Fatalf("secondo Bind: %v, atteso ErrNotFound", err)
	}
}

// TestRegistryDetachRebind: il mittente caduto resta in tolleranza, un ricevente può reclamare
// il link e il mittente che torna con il token riaggancia la sessione.
func TestRegistryDetachRebind(t *testing.T) {
	r := NewRegistry(10, time.Hour)
	r.SetSenderGrace(time.Hour)
	s, binding := reservedBound(t, r)
	if !r.Detach(s, binding) {
		t.Fatal("Detach con tolleranza deve riportare true")
	}
	select {
	case <-binding:
	default:
		t.Fatal("il canale dell'aggancio non è chiuso dopo Detach")
	}
	if !s.Away() || r.Len() != 1 {
		t.Fatalf("Away=%v, sessioni %d: attesa sessione viva senza mittente", s.Away(), r.Len())
	}
	if !r.Detach(s, binding) {
		t.Fatal("secondo Detach: atteso true (già in tolleranza)")
	}
	if _, err := r.Peek(s.Slug); err != nil {
		t.Fatalf("Peek in tolleranza: %v", err)
	}
	// Il ricevente reclama mentre il mittente è assente: attende nel canale.
	if _, err := r.Claim(s.Slug, fakeRcv{}); err != nil {
		t.Fatalf("Claim in tolleranza: %v", err)
	}
	if _, _, err := r.BindDetachable(s.Slug, strings.Repeat("0", 32)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Bind con token errato: %v", err)
	}
	s2, binding2, err := r.BindDetachable(s.Slug, s.Token)
	if err != nil || s2 != s {
		t.Fatalf("riaggancio: %v", err)
	}
	if s.Away() {
		t.Fatal("Away dopo il riaggancio")
	}
	select {
	case rcv := <-s.Attached():
		if rcv == nil {
			t.Fatal("ricevente nullo")
		}
	default:
		t.Fatal("il ricevente reclamato in tolleranza non è in coda per il nuovo aggancio")
	}
	// Un Detach del vecchio aggancio non tocca il nuovo.
	if !r.Detach(s, binding) || s.Away() {
		t.Fatal("Detach di un aggancio sostituito ha tolto il mittente nuovo")
	}
	select {
	case <-binding2:
		t.Fatal("il nuovo aggancio è stato chiuso dal Detach del vecchio")
	default:
	}
}

// TestRegistryBindTakeover: il mittente che torna prima che il server veda cadere il vecchio
// aggancio lo sostituisce; a relay iniziato no.
func TestRegistryBindTakeover(t *testing.T) {
	r := NewRegistry(10, time.Hour)
	r.SetSenderGrace(time.Hour)
	s, old := reservedBound(t, r)
	_, fresh, err := r.BindDetachable(s.Slug, s.Token)
	if err != nil {
		t.Fatalf("Bind sostitutivo: %v", err)
	}
	select {
	case <-old:
	default:
		t.Fatal("il vecchio aggancio non è stato chiuso")
	}
	s.Start()
	if _, _, err := r.BindDetachable(s.Slug, s.Token); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Bind a relay iniziato: %v, atteso ErrNotFound", err)
	}
	if r.Detach(s, fresh) {
		t.Fatal("Detach a relay iniziato deve riportare false")
	}
}

// TestRegistryGraceExpires: scaduta la tolleranza il link si chiude come mittente caduto e la
// funzione registrata viene chiamata; un ricevente in attesa vede la sessione finita.
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
		t.Fatal("Detach: atteso true")
	}
	select {
	case <-expired:
	case <-time.After(3 * time.Second):
		t.Fatal("tolleranza non scaduta entro 3 s")
	}
	select {
	case <-s.Finished():
	default:
		t.Fatal("Finished non chiuso alla scadenza della tolleranza")
	}
	if !errors.Is(s.Err(), ErrSenderGone) || r.Len() != 0 {
		t.Fatalf("Err %v, sessioni %d", s.Err(), r.Len())
	}
	if _, err := r.Peek(s.Slug); !errors.Is(err, ErrGone) {
		t.Fatalf("Peek dopo la scadenza: %v", err)
	}
}

// TestRegistryGraceRebindStopsTimer: il riaggancio ferma la scadenza della tolleranza.
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
		t.Fatalf("sessioni %d, Err %v: il riaggancio non ha fermato la scadenza", r.Len(), s.Err())
	}
}
