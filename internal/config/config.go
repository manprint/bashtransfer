// Package config legge la configurazione del server dalle variabili d'ambiente (D10).
package config

import (
	"fmt"
	"strconv"
	"time"
)

const (
	minBufSize = 4096
	maxBufSize = 4194304
)

// Config è la configurazione validata del server.
type Config struct {
	Listen       string
	PublicURL    string
	AuthUser     string
	AuthPassword string
	WaitTimeout  time.Duration
	MaxSessions  int
	BufSize      int
}

// Load costruisce la Config usando getenv (os.Getenv in produzione).
// Ogni errore nomina la variabile non valida.
func Load(getenv func(string) string) (Config, error) {
	cfg := Config{
		Listen:      withDefault(getenv("BASHT_LISTEN"), ":8080"),
		PublicURL:   getenv("BASHT_PUBLIC_URL"),
		WaitTimeout: time.Hour,
		MaxSessions: 1000,
		BufSize:     262144,
	}

	cfg.AuthUser = getenv("BASHT_AUTH_USER")
	cfg.AuthPassword = getenv("BASHT_AUTH_PASSWORD")
	if (cfg.AuthUser == "") != (cfg.AuthPassword == "") {
		return Config{}, fmt.Errorf("BASHT_AUTH_USER e BASHT_AUTH_PASSWORD devono essere impostate insieme")
	}

	if v := getenv("BASHT_WAIT_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			return Config{}, fmt.Errorf("BASHT_WAIT_TIMEOUT non valida: %q", v)
		}
		cfg.WaitTimeout = d
	}

	if v := getenv("BASHT_MAX_SESSIONS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return Config{}, fmt.Errorf("BASHT_MAX_SESSIONS non valida: %q", v)
		}
		cfg.MaxSessions = n
	}

	if v := getenv("BASHT_BUF_SIZE"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < minBufSize || n > maxBufSize {
			return Config{}, fmt.Errorf("BASHT_BUF_SIZE non valida (tra %d e %d): %q", minBufSize, maxBufSize, v)
		}
		cfg.BufSize = n
	}

	return cfg, nil
}

func withDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
