package config

import (
	"log"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds all runtime configuration, parsed once at startup from the
// environment. Missing required values are fatal so the process never starts
// in a half-configured state.
type Config struct {
	Env           string
	DatabaseURL   string
	ServerPort    int
	JWTSecret     string
	JWTAccessTTL  time.Duration
	JWTRefreshTTL time.Duration
	CORSOrigins   []string
	CookieSecure  bool
	// ReceiptPrinter is the CUPS queue name for thermal receipts. Empty means
	// use the system default destination. The queue must be a raw queue so the
	// ESC/POS bytes pass through unmodified.
	ReceiptPrinter string
}

func Load() *Config {
	c := &Config{
		Env:           getEnv("APP_ENV", "development"),
		DatabaseURL:   mustEnv("DATABASE_URL"),
		ServerPort:    mustInt("SERVER_PORT", 3000),
		JWTSecret:     mustEnv("JWT_SECRET"),
		JWTAccessTTL:  mustDuration("JWT_EXPIRES_IN", 15*time.Minute),
		JWTRefreshTTL: mustDuration("JWT_REFRESH_EXPIRES_IN", 7*24*time.Hour),
		CORSOrigins:   strings.Split(getEnv("CORS_ORIGINS", "http://localhost:3000"), ","),
		ReceiptPrinter: getEnv("RECEIPT_PRINTER", ""),
	}
	c.CookieSecure = c.Env == "production"
	if len(c.JWTSecret) < 32 {
		log.Fatal("JWT_SECRET must be at least 32 characters")
	}
	return c
}

func (c *Config) IsProd() bool { return c.Env == "production" }

func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		log.Fatalf("required env var %s is not set", key)
	}
	return v
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func mustInt(key string, fallback int) int {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		log.Fatalf("%s must be an integer", key)
	}
	return n
}

func mustDuration(key string, fallback time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		log.Fatalf("%s must be a duration (e.g. 15m, 168h)", key)
	}
	return d
}
