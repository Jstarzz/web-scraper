package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

type Config struct {
	APIAddr                 string
	DatabaseURL             string
	AdminToken              string
	RetentionDays           int
	WorkerID                string
	WorkerConcurrency       int
	WorkerPollInterval      time.Duration
	WorkerMaxAttempts       int
	WorkerRetryBase         time.Duration
	WorkerHeartbeatInterval time.Duration
	WorkerStaleAfter        time.Duration
	BrowserWorkerURL        string
	BrowserRequestTimeout   time.Duration
}

func Load() (Config, error) {
	cfg := Config{
		APIAddr:                 env("API_ADDR", ":8080"),
		DatabaseURL:             os.Getenv("DATABASE_URL"),
		AdminToken:              os.Getenv("ADMIN_TOKEN"),
		RetentionDays:           envInt("RETENTION_DAYS", 60),
		WorkerID:                env("WORKER_ID", hostname()),
		WorkerConcurrency:       envInt("WORKER_CONCURRENCY", 4),
		WorkerPollInterval:      envDuration("WORKER_POLL_INTERVAL", 200*time.Millisecond),
		WorkerMaxAttempts:       envInt("WORKER_MAX_ATTEMPTS", 3),
		WorkerRetryBase:         envDuration("WORKER_RETRY_BASE", time.Second),
		WorkerHeartbeatInterval: envDuration("WORKER_HEARTBEAT_INTERVAL", 20*time.Second),
		WorkerStaleAfter:        envDuration("WORKER_STALE_AFTER", 90*time.Second),
		BrowserWorkerURL:        env("BROWSER_WORKER_URL", "http://browser-worker:3000"),
		BrowserRequestTimeout:   envDuration("BROWSER_REQUEST_TIMEOUT", 45*time.Second),
	}
	if cfg.DatabaseURL == "" {
		return Config{}, fmt.Errorf("DATABASE_URL is required")
	}
	if cfg.RetentionDays < 1 {
		return Config{}, fmt.Errorf("RETENTION_DAYS must be >= 1")
	}
	if cfg.WorkerConcurrency < 1 || cfg.WorkerConcurrency > 64 {
		return Config{}, fmt.Errorf("WORKER_CONCURRENCY must be between 1 and 64")
	}
	if cfg.WorkerPollInterval < 50*time.Millisecond || cfg.WorkerPollInterval > 30*time.Second {
		return Config{}, fmt.Errorf("WORKER_POLL_INTERVAL must be between 50ms and 30s")
	}
	if cfg.WorkerMaxAttempts < 1 || cfg.WorkerMaxAttempts > 5 {
		return Config{}, fmt.Errorf("WORKER_MAX_ATTEMPTS must be between 1 and 5")
	}
	if cfg.WorkerRetryBase < 100*time.Millisecond || cfg.WorkerRetryBase > 30*time.Second {
		return Config{}, fmt.Errorf("WORKER_RETRY_BASE must be between 100ms and 30s")
	}
	if cfg.WorkerHeartbeatInterval < 5*time.Second || cfg.WorkerHeartbeatInterval > time.Minute {
		return Config{}, fmt.Errorf("WORKER_HEARTBEAT_INTERVAL must be between 5s and 1m")
	}
	if cfg.WorkerStaleAfter < 3*cfg.WorkerHeartbeatInterval {
		return Config{}, fmt.Errorf("WORKER_STALE_AFTER must be at least 3x WORKER_HEARTBEAT_INTERVAL")
	}
	if cfg.BrowserRequestTimeout < 5*time.Second || cfg.BrowserRequestTimeout > 5*time.Minute {
		return Config{}, fmt.Errorf("BROWSER_REQUEST_TIMEOUT must be between 5s and 5m")
	}
	return cfg, nil
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func envInt(key string, fallback int) int {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func envDuration(key string, fallback time.Duration) time.Duration {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func hostname() string {
	name, err := os.Hostname()
	if err != nil || name == "" {
		return "worker"
	}
	return name
}
