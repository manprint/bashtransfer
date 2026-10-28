package relay

import (
	"errors"
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
