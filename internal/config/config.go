// Package config loads application configuration from environment variables.
package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// Config holds every runtime setting of the service.
// Nothing is hardcoded: the process refuses to start if a secret is missing.
type Config struct {
	Addr        string
	DatabaseURL string
	APIToken    string

	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration

	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	ShutdownTimeout   time.Duration

	// StartupTimeout bounds how long the process waits for the database to
	// become reachable before giving up. Keep it under the time the
	// Kubernetes startupProbe allows (periodSeconds * failureThreshold),
	// otherwise the kubelet kills the pod mid-wait.
	StartupTimeout time.Duration
}

// Load reads configuration from the environment and validates it.
func Load() (Config, error) {
	cfg := Config{
		Addr:        env("API_ADDR", ":8080"),
		DatabaseURL: os.Getenv("DATABASE_URL"),
		APIToken:    os.Getenv("API_TOKEN"),

		MaxOpenConns:    envInt("DB_MAX_OPEN_CONNS", 25),
		MaxIdleConns:    envInt("DB_MAX_IDLE_CONNS", 25),
		ConnMaxLifetime: envDuration("DB_CONN_MAX_LIFETIME", 5*time.Minute),

		ReadHeaderTimeout: envDuration("HTTP_READ_HEADER_TIMEOUT", 5*time.Second),
		ReadTimeout:       envDuration("HTTP_READ_TIMEOUT", 10*time.Second),
		WriteTimeout:      envDuration("HTTP_WRITE_TIMEOUT", 15*time.Second),
		IdleTimeout:       envDuration("HTTP_IDLE_TIMEOUT", 60*time.Second),
		ShutdownTimeout:   envDuration("HTTP_SHUTDOWN_TIMEOUT", 10*time.Second),

		StartupTimeout: envDuration("DB_STARTUP_TIMEOUT", 20*time.Second),
	}

	if cfg.DatabaseURL == "" {
		return Config{}, fmt.Errorf("DATABASE_URL is required")
	}
	if cfg.APIToken == "" {
		return Config{}, fmt.Errorf("API_TOKEN is required")
	}

	return cfg, nil
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	v, err := strconv.Atoi(os.Getenv(key))
	if err != nil || v <= 0 {
		return fallback
	}
	return v
}

func envDuration(key string, fallback time.Duration) time.Duration {
	d, err := time.ParseDuration(os.Getenv(key))
	if err != nil || d <= 0 {
		return fallback
	}
	return d
}
