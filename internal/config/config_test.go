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
