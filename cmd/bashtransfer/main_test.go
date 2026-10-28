package main

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestHealthURL(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", "http://127.0.0.1:8080/"},
		{":8080", "http://127.0.0.1:8080/"},
		{"0.0.0.0:9000", "http://127.0.0.1:9000/"},
		{"[::]:9000", "http://127.0.0.1:9000/"},
		{"127.0.0.1:1234", "http://127.0.0.1:1234/"},
		{"[::1]:1234", "http://[::1]:1234/"},
	}
	for _, c := range cases {
		got, err := healthURL(c.in)
		if err != nil || got != c.want {
			t.Errorf("healthURL(%q) = %q, %v; atteso %q", c.in, got, err, c.want)
		}
	}
	if _, err := healthURL("senza-porta"); err == nil {
		t.Error("healthURL senza porta: atteso errore")
	}
}

func TestCheckHealth(t *testing.T) {
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) }))
	defer srv.Close()
	addr := srv.Listener.Addr().String()

	if err := checkHealth(addr); err != nil {
		t.Fatalf("server sano: %v", err)
	}
	status = http.StatusInternalServerError
	if err := checkHealth(addr); err == nil {
		t.Fatal("stato 500: atteso errore")
	}

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := l.Addr().String()
	l.Close()
	if err := checkHealth(closed); err == nil {
		t.Fatal("porta chiusa: atteso errore")
	}
}

// TestSecondSignalExits: con un upload in attesa lo shutdown ordinato dura shutdownTimeout; un
// secondo SIGINT termina subito il processo. Il server gira in un sottoprocesso (run reale).
func TestSecondSignalExits(t *testing.T) {
	if os.Getenv("BT_TEST_RUN_MAIN") == "1" {
		if err := run(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	if runtime.GOOS == "windows" {
		t.Skip("SIGINT non inviabile a un processo su Windows")
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()

	cmd := exec.Command(os.Args[0], "-test.run=^TestSecondSignalExits$")
	cmd.Env = append(os.Environ(), "BT_TEST_RUN_MAIN=1", "BASHT_LISTEN="+addr)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	lines := make(chan string, 16)
	go func() {
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()
	waitLine := func(substr string) {
		t.Helper()
		deadline := time.After(5 * time.Second)
		for {
			select {
			case l, ok := <-lines:
				if !ok {
					t.Fatalf("processo terminato prima di %q", substr)
				}
				if strings.Contains(l, substr) {
					return
				}
			case <-deadline:
				t.Fatalf("nessuna riga %q entro 5 s", substr)
			}
		}
	}
	waitLine("in ascolto")

	// Upload in attesa: connessione aperta, body dichiarato e non inviato.
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprintf(conn, "PUT /f.bin HTTP/1.1\r\nHost: x\r\nContent-Length: 100\r\n\r\n")
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := http.ReadResponse(bufio.NewReader(conn), nil); err != nil {
		t.Fatalf("risposta dell'upload: %v", err)
	}

	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	waitLine("arresto in corso")
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			t.Fatalf("uscita: %v, atteso termine per segnale", err)
		}
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok && !ws.Signaled() {
			t.Fatalf("uscita con codice %d, atteso termine per segnale", ws.ExitStatus())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("il secondo SIGINT non ha terminato il processo")
	}
}
