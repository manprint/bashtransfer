package config

import (
	"log/slog"
	"strings"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load(env(nil))
	if err != nil {
		t.Fatalf("Load without variables: %v", err)
	}
	if cfg.Listen != ":8080" || cfg.PublicURL != "" || cfg.AuthUser != "" || cfg.AuthPassword != "" {
		t.Errorf("wrong string defaults: %+v", cfg)
	}
	if cfg.WaitTimeout != time.Hour || cfg.MaxSessions != 1000 || cfg.BufSize != 262144 {
		t.Errorf("wrong numeric defaults: %+v", cfg)
	}
}

func TestLoadAuthPairRequired(t *testing.T) {
	for _, m := range []map[string]string{
		{"BASHT_AUTH_USER": "u"},
		{"BASHT_AUTH_PASSWORD": "p"},
	} {
		_, err := Load(env(m))
		if err == nil {
			t.Fatalf("expected error for %v", m)
		}
		if !strings.Contains(err.Error(), "BASHT_AUTH_USER") {
			t.Errorf("message without variable name: %v", err)
		}
	}
	cfg, err := Load(env(map[string]string{"BASHT_AUTH_USER": "u", "BASHT_AUTH_PASSWORD": "p"}))
	if err != nil || cfg.AuthUser != "u" || cfg.AuthPassword != "p" {
		t.Errorf("valid pair rejected: %+v %v", cfg, err)
	}
}

func TestConfigIdleTimeout(t *testing.T) {
	cfg, err := Load(env(nil))
	if err != nil || cfg.IdleTimeout != 2*time.Minute {
		t.Fatalf("default idle: %v %v", cfg.IdleTimeout, err)
	}
	for _, v := range []string{"0s", "500ms"} {
		_, err := Load(env(map[string]string{"BASHT_IDLE_TIMEOUT": v}))
		if err == nil || !strings.Contains(err.Error(), "BASHT_IDLE_TIMEOUT") {
			t.Errorf("BASHT_IDLE_TIMEOUT=%s: error %v, want error with variable name", v, err)
		}
	}
}

func TestLoadRejectsBadNumbers(t *testing.T) {
	cases := []struct {
		name string
		key  string
		val  string
	}{
		{"unparsable timeout", "BASHT_WAIT_TIMEOUT", "abc"},
		{"zero timeout", "BASHT_WAIT_TIMEOUT", "0s"},
		{"zero sessions", "BASHT_MAX_SESSIONS", "0"},
		{"buffer below minimum", "BASHT_BUF_SIZE", "1024"},
		{"buffer above maximum", "BASHT_BUF_SIZE", "8388609"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Load(env(map[string]string{c.key: c.val}))
			if err == nil {
				t.Fatalf("expected error for %s=%s", c.key, c.val)
			}
			if !strings.Contains(err.Error(), c.key) {
				t.Errorf("message without variable name: %v", err)
			}
		})
	}
}

func TestLoadPublicURL(t *testing.T) {
	ok := []string{"https://bashtransfer.mydomain.it", "https://host.it/", "http://127.0.0.1:8080", "https://host.it/prefisso"}
	for _, v := range ok {
		if _, err := Load(env(map[string]string{"BASHT_PUBLIC_URL": v})); err != nil {
			t.Errorf("BASHT_PUBLIC_URL=%q: %v", v, err)
		}
	}
	bad := []string{"bashtransfer.mydomain.it", "ftp://host.it", "https://", "https://host.it?x=1", "https://host.it/#f",
		"https://u:p@host.it", `https://host.it/"x`, "https://host.it/$(id)", "https://host.it/`id`", "https://host.it/a b"}
	for _, v := range bad {
		_, err := Load(env(map[string]string{"BASHT_PUBLIC_URL": v}))
		if err == nil {
			t.Errorf("BASHT_PUBLIC_URL=%q accepted, expected rejection", v)
			continue
		}
		if !strings.Contains(err.Error(), "BASHT_PUBLIC_URL") {
			t.Errorf("error without variable name: %v", err)
		}
	}
}

func TestLoadLogging(t *testing.T) {
	cfg, err := Load(env(nil))
	if err != nil || cfg.LogFormat != LogText || cfg.LogLevel != slog.LevelInfo {
		t.Fatalf("default log: %q %v %v", cfg.LogFormat, cfg.LogLevel, err)
	}
	cfg, err = Load(env(map[string]string{"BASHT_LOG_FORMAT": "JSON", "BASHT_LOG_LEVEL": "debug"}))
	if err != nil || cfg.LogFormat != LogJSON || cfg.LogLevel != slog.LevelDebug {
		t.Fatalf("json/debug: %q %v %v", cfg.LogFormat, cfg.LogLevel, err)
	}
	for _, m := range []map[string]string{
		{"BASHT_LOG_FORMAT": "xml"},
		{"BASHT_LOG_LEVEL": "verbose"},
	} {
		_, err := Load(env(m))
		if err == nil {
			t.Errorf("%v: expected error", m)
			continue
		}
		for k := range m {
			if !strings.Contains(err.Error(), k) {
				t.Errorf("error %q does not name %s", err, k)
			}
		}
	}
}

// TestConfigSenderGrace: whole seconds; default 120, 0 disables the tolerance.
func TestConfigSenderGrace(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  time.Duration
	}{{"", 2 * time.Minute}, {"0", 0}, {"120", 2 * time.Minute}} {
		cfg, err := Load(env(map[string]string{"BASHT_SENDER_GRACE": tc.value}))
		if err != nil || cfg.SenderGrace != tc.want {
			t.Errorf("BASHT_SENDER_GRACE=%q: %v %v, want %v", tc.value, cfg.SenderGrace, err, tc.want)
		}
	}
	for _, v := range []string{"-1", "1.5", "2m", "abc", "86401"} {
		_, err := Load(env(map[string]string{"BASHT_SENDER_GRACE": v}))
		if err == nil || !strings.Contains(err.Error(), "BASHT_SENDER_GRACE") {
			t.Errorf("BASHT_SENDER_GRACE=%s: error %v, want error with variable name", v, err)
		}
	}
}
