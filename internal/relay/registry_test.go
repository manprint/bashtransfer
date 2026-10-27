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
