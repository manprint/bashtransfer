// Package config legge la configurazione del server dalle variabili d'ambiente (D10).
package config

import (
	"fmt"
	"log/slog"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	minBufSize  = 4096
	maxBufSize  = 4194304
	minIdleTime = time.Second
)

// Config è la configurazione validata del server.
type Config struct {
	Listen       string
	PublicURL    string
	AuthUser     string
	AuthPassword string
	WaitTimeout  time.Duration
	IdleTimeout  time.Duration
	MaxSessions  int
	BufSize      int
	// LogFormat è "text" (logfmt, una riga chiave=valore) o "json" (un oggetto per riga).
	LogFormat string
	LogLevel  slog.Level
}

// Formati di log accettati da BASHT_LOG_FORMAT.
const (
	LogText = "text"
	LogJSON = "json"
)

// Load costruisce la Config usando getenv (os.Getenv in produzione).
// Ogni errore nomina la variabile non valida.
func Load(getenv func(string) string) (Config, error) {
	cfg := Config{
		Listen:      withDefault(getenv("BASHT_LISTEN"), ":8080"),
		PublicURL:   getenv("BASHT_PUBLIC_URL"),
		WaitTimeout: time.Hour,
		IdleTimeout: 2 * time.Minute,
		MaxSessions: 1000,
		BufSize:     262144,
		LogFormat:   LogText,
		LogLevel:    slog.LevelInfo,
	}

	if cfg.PublicURL != "" {
		if err := validatePublicURL(cfg.PublicURL); err != nil {
			return Config{}, fmt.Errorf("BASHT_PUBLIC_URL non valida: %q: %w", cfg.PublicURL, err)
		}
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

	if v := getenv("BASHT_IDLE_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d < minIdleTime {
			return Config{}, fmt.Errorf("BASHT_IDLE_TIMEOUT non valida (minimo %s): %q", minIdleTime, v)
		}
		cfg.IdleTimeout = d
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

	switch v := strings.ToLower(getenv("BASHT_LOG_FORMAT")); v {
	case "":
	case LogText, LogJSON:
		cfg.LogFormat = v
	default:
		return Config{}, fmt.Errorf("BASHT_LOG_FORMAT non valida (text o json): %q", v)
	}

	if v := getenv("BASHT_LOG_LEVEL"); v != "" {
		// UnmarshalText accetta debug, info, warn, error (anche maiuscoli e con offset, es. info+2).
		if err := cfg.LogLevel.UnmarshalText([]byte(v)); err != nil {
			return Config{}, fmt.Errorf("BASHT_LOG_LEVEL non valida (debug, info, warn, error): %q", v)
		}
	}

	return cfg, nil
}

// validatePublicURL richiede una base http(s) senza query né frammento. L'URL finisce tra
// virgolette doppie nei comandi stampati: caratteri che la shell interpreta sono rifiutati.
func validatePublicURL(v string) error {
	u, err := url.Parse(v)
	if err != nil {
		return err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("schema http o https richiesto")
	}
	if u.Host == "" {
		return fmt.Errorf("host mancante")
	}
	if u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return fmt.Errorf("query, frammento e credenziali non ammessi")
	}
	if strings.ContainsAny(v, "\"`$\\ !\t\r\n") {
		return fmt.Errorf("caratteri non ammessi")
	}
	return nil
}

func withDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
