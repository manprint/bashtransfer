package config

import (
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
		t.Fatalf("Load senza variabili: %v", err)
	}
	if cfg.Listen != ":8080" || cfg.PublicURL != "" || cfg.AuthUser != "" || cfg.AuthPassword != "" {
		t.Errorf("default stringhe errati: %+v", cfg)
	}
	if cfg.WaitTimeout != time.Hour || cfg.MaxSessions != 1000 || cfg.BufSize != 262144 {
		t.Errorf("default numerici errati: %+v", cfg)
	}
}

func TestLoadAuthPairRequired(t *testing.T) {
	for _, m := range []map[string]string{
		{"BASHT_AUTH_USER": "u"},
		{"BASHT_AUTH_PASSWORD": "p"},
	} {
		_, err := Load(env(m))
		if err == nil {
			t.Fatalf("attesa errore per %v", m)
		}
		if !strings.Contains(err.Error(), "BASHT_AUTH_USER") {
			t.Errorf("messaggio senza nome variabile: %v", err)
		}
	}
	cfg, err := Load(env(map[string]string{"BASHT_AUTH_USER": "u", "BASHT_AUTH_PASSWORD": "p"}))
	if err != nil || cfg.AuthUser != "u" || cfg.AuthPassword != "p" {
		t.Errorf("coppia valida rifiutata: %+v %v", cfg, err)
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
			t.Errorf("BASHT_IDLE_TIMEOUT=%s: errore %v, atteso errore con nome variabile", v, err)
		}
	}
}

func TestLoadRejectsBadNumbers(t *testing.T) {
	cases := []struct {
		name string
		key  string
		val  string
	}{
		{"timeout non parsabile", "BASHT_WAIT_TIMEOUT", "abc"},
		{"timeout zero", "BASHT_WAIT_TIMEOUT", "0s"},
		{"sessioni zero", "BASHT_MAX_SESSIONS", "0"},
		{"buffer sotto minimo", "BASHT_BUF_SIZE", "1024"},
		{"buffer sopra massimo", "BASHT_BUF_SIZE", "8388609"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Load(env(map[string]string{c.key: c.val}))
			if err == nil {
				t.Fatalf("attesa errore per %s=%s", c.key, c.val)
			}
			if !strings.Contains(err.Error(), c.key) {
				t.Errorf("messaggio senza nome variabile: %v", err)
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
			t.Errorf("BASHT_PUBLIC_URL=%q accettata, attesa rifiutata", v)
			continue
		}
		if !strings.Contains(err.Error(), "BASHT_PUBLIC_URL") {
			t.Errorf("errore senza nome della variabile: %v", err)
		}
	}
}
