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
			t.Errorf("healthURL(%q) = %q, %v; want %q", c.in, got, err, c.want)
		}
	}
	if _, err := healthURL("no-port"); err == nil {
		t.Error("healthURL without port: expected error")
	}
}

func TestCheckHealth(t *testing.T) {
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) }))
	defer srv.Close()
	addr := srv.Listener.Addr().String()

	if err := checkHealth(addr); err != nil {
		t.Fatalf("healthy server: %v", err)
	}
	status = http.StatusInternalServerError
	if err := checkHealth(addr); err == nil {
		t.Fatal("status 500: expected error")
	}

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := l.Addr().String()
	l.Close()
	if err := checkHealth(closed); err == nil {
		t.Fatal("closed port: expected error")
	}
}

// runChild runs run in the subprocess started by startServer and terminates the process.
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

// startServer starts run in a subprocess that runs only the calling test and waits for
// server_listening.
func startServer(t *testing.T, testName string) *child {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("SIGINT cannot be sent to a process on Windows")
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
				t.Fatalf("process ended before %q", substr)
			}
			if strings.Contains(l, substr) {
				return
			}
		case <-deadline:
			t.Fatalf("no %q line within 5 s", substr)
		}
	}
}

// waitingUpload opens an upload with a declared body that is not sent, and returns its slug.
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
		t.Fatalf("upload response: %v", err)
	}
	// The response is a table: the curl row ("| curl   | curl -f -OJ ... |") follows the header.
	br := bufio.NewReader(resp.Body)
	var line string
	for !strings.HasPrefix(line, "| curl ") {
		if line, err = br.ReadString('\n'); err != nil {
			t.Fatalf("curl line: %v", err)
		}
	}
	_ = conn.SetReadDeadline(time.Time{})
	line = strings.TrimSuffix(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(line), "|")), `"`)
	return conn, line[strings.LastIndex(line, "/")+1:]
}

// TestSecondSignalExits: with a transfer in progress the graceful shutdown lasts until
// shutdownTimeout; a second SIGINT terminates the process immediately. The server runs in a
// subprocess (real run).
func TestSecondSignalExits(t *testing.T) {
	runChild()
	c := startServer(t, "TestSecondSignalExits")

	// Transfer in progress: slug claimed, sender that does not send the body.
	_, slug := waitingUpload(t, c.addr)
	dl, err := net.Dial("tcp", c.addr)
	if err != nil {
		t.Fatal(err)
	}
	defer dl.Close()
	fmt.Fprintf(dl, "GET /%s HTTP/1.1\r\nHost: x\r\n\r\n", slug)
	_ = dl.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := http.ReadResponse(bufio.NewReader(dl), nil); err != nil {
		t.Fatalf("download response: %v", err)
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
			t.Fatalf("exit: %v, expected termination by signal", err)
		}
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok && !ws.Signaled() {
			t.Fatalf("exit with code %d, expected termination by signal", ws.ExitStatus())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the second SIGINT did not terminate the process")
	}
}

// TestShutdownExpiresWaitingSenders: on shutdown, senders still waiting for a receiver are
// closed immediately: the process exits successfully well before shutdownTimeout.
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
			t.Fatalf("exit: %v, want 0", err)
		}
		if d := time.Since(start); d >= shutdownTimeout/2 {
			t.Fatalf("shutdown in %s with a single waiting sender", d)
		}
	case <-time.After(shutdownTimeout / 2):
		t.Fatal("the waiting sender holds up the shutdown")
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.Copy(io.Discard, conn); err != nil {
		t.Fatalf("sender connection not closed: %v", err)
	}
}

// TestNewLogger: json produces one object per line with time, level and msg; the level filters.
func TestNewLogger(t *testing.T) {
	var buf strings.Builder
	l := newLogger(&buf, config.LogJSON, slog.LevelInfo)
	l.Debug("nascosto")
	l.Info("evento", "k", 1)
	var m map[string]any
	if err := json.Unmarshal([]byte(buf.String()), &m); err != nil {
		t.Fatalf("non-JSON line %q: %v", buf.String(), err)
	}
	if m["msg"] != "evento" || m["level"] != "INFO" || m["k"] != float64(1) || m["time"] == nil {
		t.Fatalf("JSON fields: %v", m)
	}
	buf.Reset()
	newLogger(&buf, config.LogText, slog.LevelWarn).Info("nascosto")
	newLogger(&buf, config.LogText, slog.LevelWarn).Warn("visibile", "k", "v")
	if out := buf.String(); strings.Contains(out, "nascosto") || !strings.Contains(out, "msg=visibile k=v") {
		t.Fatalf("text: %q", out)
	}
}
