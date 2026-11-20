// Package config reads service configuration from the environment.
// Secrets are read from files referenced by *_FILE variables so they never show up in `docker inspect`.
package config

import (
	"fmt"
	"net/url"
	"os"
	"strings"
)

type Config struct {
	HTTPAddr   string
	DB         DB
	LogLevel   string
	AuditReads bool
}

type DB struct {
	Host     string
	Port     string
	Name     string
	User     string
	Password string
	// URL overrides the individual fields when set (KN_DB_URL).
	URL string
}

// DSN returns a libpq-style connection URL.
func (d DB) DSN() string {
	if d.URL != "" {
		return d.URL
	}
	u := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(d.User, d.Password),
		Host:     d.Host + ":" + d.Port,
		Path:     "/" + d.Name,
		RawQuery: "sslmode=disable",
	}
	return u.String()
}

func Load() (Config, error) {
	pw, err := secret("KN_DB_PASSWORD")
	if err != nil {
		return Config{}, err
	}
	return Config{
		HTTPAddr: env("KN_HTTP_ADDR", "127.0.0.1:8787"),
		DB: DB{
			Host:     env("KN_DB_HOST", "127.0.0.1"),
			Port:     env("KN_DB_PORT", "5432"),
			Name:     env("KN_DB_NAME", "knowledge"),
			User:     env("KN_DB_USER", "kn_app"),
			Password: pw,
			URL:      os.Getenv("KN_DB_URL"),
		},
		LogLevel:   env("KN_LOG_LEVEL", "info"),
		AuditReads: env("KN_AUDIT_READS", "false") == "true",
	}, nil
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// secret returns $KEY, or the trimmed contents of the file named by $KEY_FILE.
func secret(key string) (string, error) {
	if v := os.Getenv(key); v != "" {
		return v, nil
	}
	path := os.Getenv(key + "_FILE")
	if path == "" {
		return "", nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s_FILE: %w", key, err)
	}
	return strings.TrimSpace(string(b)), nil
}
