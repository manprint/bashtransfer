// Package config reads the server configuration from environment variables (D10).
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
	// maxSenderGraceSeconds (one day) avoids a value that overflows time.Duration or keeps
	// orphaned sessions beyond any purpose.
	maxSenderGraceSeconds = 86400
)

// Config is the validated server configuration.
type Config struct {
	Listen       string
	PublicURL    string
	AuthUser     string
	AuthPassword string
	WaitTimeout  time.Duration
	IdleTimeout  time.Duration
	// If > 0, keeps a sender's reservation alive for that long after the browser
	// dropped before the receiver (BASHT_SENDER_GRACE, seconds). 0 disables it.
	SenderGrace time.Duration
	MaxSessions int
	BufSize     int
	// LogFormat is "text" (logfmt, one key=value line) or "json" (one object per line).
	LogFormat string
	LogLevel  slog.Level
}

// Log formats accepted by BASHT_LOG_FORMAT.
const (
	LogText = "text"
	LogJSON = "json"
)

// Load builds the Config using getenv (os.Getenv in production).
// Every error names the invalid variable.
func Load(getenv func(string) string) (Config, error) {
	cfg := Config{
		Listen:      withDefault(getenv("BASHT_LISTEN"), ":8080"),
		PublicURL:   getenv("BASHT_PUBLIC_URL"),
		WaitTimeout: time.Hour,
		IdleTimeout: 2 * time.Minute,
		SenderGrace: 2 * time.Minute,
		MaxSessions: 1000,
		BufSize:     262144,
		LogFormat:   LogText,
		LogLevel:    slog.LevelInfo,
	}

	if cfg.PublicURL != "" {
		if err := validatePublicURL(cfg.PublicURL); err != nil {
			return Config{}, fmt.Errorf("BASHT_PUBLIC_URL invalid: %q: %w", cfg.PublicURL, err)
		}
	}

	cfg.AuthUser = getenv("BASHT_AUTH_USER")
	cfg.AuthPassword = getenv("BASHT_AUTH_PASSWORD")
	if (cfg.AuthUser == "") != (cfg.AuthPassword == "") {
		return Config{}, fmt.Errorf("BASHT_AUTH_USER and BASHT_AUTH_PASSWORD must be set together")
	}

	if v := getenv("BASHT_WAIT_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			return Config{}, fmt.Errorf("BASHT_WAIT_TIMEOUT invalid: %q", v)
		}
		cfg.WaitTimeout = d
	}

	if v := getenv("BASHT_IDLE_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d < minIdleTime {
			return Config{}, fmt.Errorf("BASHT_IDLE_TIMEOUT invalid (minimum %s): %q", minIdleTime, v)
		}
		cfg.IdleTimeout = d
	}

	// Whole seconds; 0 disables the tolerance.
	if v := getenv("BASHT_SENDER_GRACE"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 || n > maxSenderGraceSeconds {
			return Config{}, fmt.Errorf("BASHT_SENDER_GRACE invalid (whole seconds between 0 and %d): %q", maxSenderGraceSeconds, v)
		}
		cfg.SenderGrace = time.Duration(n) * time.Second
	}

	if v := getenv("BASHT_MAX_SESSIONS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return Config{}, fmt.Errorf("BASHT_MAX_SESSIONS invalid: %q", v)
		}
		cfg.MaxSessions = n
	}

	if v := getenv("BASHT_BUF_SIZE"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < minBufSize || n > maxBufSize {
			return Config{}, fmt.Errorf("BASHT_BUF_SIZE invalid (between %d and %d): %q", minBufSize, maxBufSize, v)
		}
		cfg.BufSize = n
	}

	switch v := strings.ToLower(getenv("BASHT_LOG_FORMAT")); v {
	case "":
	case LogText, LogJSON:
		cfg.LogFormat = v
	default:
		return Config{}, fmt.Errorf("BASHT_LOG_FORMAT invalid (text or json): %q", v)
	}

	if v := getenv("BASHT_LOG_LEVEL"); v != "" {
		// UnmarshalText accepts debug, info, warn, error (also uppercase and with offsets, e.g. info+2).
		if err := cfg.LogLevel.UnmarshalText([]byte(v)); err != nil {
			return Config{}, fmt.Errorf("BASHT_LOG_LEVEL invalid (debug, info, warn, error): %q", v)
		}
	}

	return cfg, nil
}

// validatePublicURL requires an http(s) base without query or fragment. The URL ends up in double
// quotes in the printed commands: characters that the shell interprets are rejected.
func validatePublicURL(v string) error {
	u, err := url.Parse(v)
	if err != nil {
		return err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("scheme http or https required")
	}
	if u.Host == "" {
		return fmt.Errorf("missing host")
	}
	if u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return fmt.Errorf("query, fragment and credentials not allowed")
	}
	if strings.ContainsAny(v, "\"`$\\ !\t\r\n") {
		return fmt.Errorf("characters not allowed")
	}
	return nil
}

func withDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
