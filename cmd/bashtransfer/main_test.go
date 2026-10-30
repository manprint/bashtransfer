package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
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

	"bashtransfer/internal/config"
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

// runChild esegue run nel sottoprocesso avviato da startServer e ne termina il processo.
func runChild() {
	if os.Getenv("BT_TEST_RUN_MAIN") != "1" {
		return
	}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

type child struct {
	cmd   *exec.Cmd
	addr  string
	lines chan string
}

// startServer avvia run in un sottoprocesso che esegue solo il test chiamante e attende
// server_listening.
func startServer(t *testing.T, testName string) *child {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("SIGINT non inviabile a un processo su Windows")
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()

	cmd := exec.Command(os.Args[0], "-test.run=^"+testName+"$")
	cmd.Env = append(os.Environ(), "BT_TEST_RUN_MAIN=1", "BASHT_LISTEN="+addr)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	c := &child{cmd: cmd, addr: addr, lines: make(chan string, 16)}
	go func() {
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			c.lines <- sc.Text()
		}
		close(c.lines)
	}()
	c.waitLine(t, "server_listening")
	return c
}

func (c *child) waitLine(t *testing.T, substr string) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case l, ok := <-c.lines:
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

// waitingUpload apre un upload con body dichiarato e non inviato e ne restituisce lo slug.
func waitingUpload(t *testing.T, addr string) (net.Conn, string) {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	fmt.Fprintf(conn, "PUT /f.bin HTTP/1.1\r\nHost: x\r\nContent-Length: 100\r\n\r\n")
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("risposta dell'upload: %v", err)
	}
	// La risposta è una tabella: la riga curl segue l'intestazione.
	br := bufio.NewReader(resp.Body)
	var line string
	for !strings.HasPrefix(line, "curl -o") {
		if line, err = br.ReadString('\n'); err != nil {
			t.Fatalf("riga curl: %v", err)
		}
	}
	_ = conn.SetReadDeadline(time.Time{})
	line = strings.TrimSuffix(strings.TrimSpace(line), `"`)
	return conn, line[strings.LastIndex(line, "/")+1:]
}

// TestSecondSignalExits: con un trasferimento in corso lo shutdown ordinato dura fino a
// shutdownTimeout; un secondo SIGINT termina subito il processo. Il server gira in un
// sottoprocesso (run reale).
func TestSecondSignalExits(t *testing.T) {
	runChild()
	c := startServer(t, "TestSecondSignalExits")

	// Trasferimento in corso: slug reclamato, mittente che non invia il body.
	_, slug := waitingUpload(t, c.addr)
	dl, err := net.Dial("tcp", c.addr)
	if err != nil {
		t.Fatal(err)
	}
	defer dl.Close()
	fmt.Fprintf(dl, "GET /%s HTTP/1.1\r\nHost: x\r\n\r\n", slug)
	_ = dl.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := http.ReadResponse(bufio.NewReader(dl), nil); err != nil {
		t.Fatalf("risposta del download: %v", err)
	}

	if err := c.cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	c.waitLine(t, "shutdown_start")
	if err := c.cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- c.cmd.Wait() }()
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

// TestShutdownExpiresWaitingSenders: all'arresto i mittenti ancora senza ricevente vengono
// chiusi subito: il processo esce con successo ben prima di shutdownTimeout.
func TestShutdownExpiresWaitingSenders(t *testing.T) {
	runChild()
	c := startServer(t, "TestShutdownExpiresWaitingSenders")
	conn, _ := waitingUpload(t, c.addr)

	start := time.Now()
	if err := c.cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- c.cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("uscita: %v, attesa 0", err)
		}
		if d := time.Since(start); d >= shutdownTimeout/2 {
			t.Fatalf("arresto in %s con un solo mittente in attesa", d)
		}
	case <-time.After(shutdownTimeout / 2):
		t.Fatal("il mittente in attesa trattiene l'arresto")
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.Copy(io.Discard, conn); err != nil {
		t.Fatalf("connessione del mittente non chiusa: %v", err)
	}
}

// TestNewLogger: json produce un oggetto per riga con time, level e msg; il livello filtra.
func TestNewLogger(t *testing.T) {
	var buf strings.Builder
	l := newLogger(&buf, config.LogJSON, slog.LevelInfo)
	l.Debug("nascosto")
	l.Info("evento", "k", 1)
	var m map[string]any
	if err := json.Unmarshal([]byte(buf.String()), &m); err != nil {
		t.Fatalf("riga non JSON %q: %v", buf.String(), err)
	}
	if m["msg"] != "evento" || m["level"] != "INFO" || m["k"] != float64(1) || m["time"] == nil {
		t.Fatalf("campi JSON: %v", m)
	}
	buf.Reset()
	newLogger(&buf, config.LogText, slog.LevelWarn).Info("nascosto")
	newLogger(&buf, config.LogText, slog.LevelWarn).Warn("visibile", "k", "v")
	if out := buf.String(); strings.Contains(out, "nascosto") || !strings.Contains(out, "msg=visibile k=v") {
		t.Fatalf("text: %q", out)
	}
}
