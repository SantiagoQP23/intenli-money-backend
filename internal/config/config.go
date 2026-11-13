// Package config loads runtime configuration from environment variables.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
)

const (
	EnvDevelopment = "development"
	EnvProduction  = "production"
)

type Config struct {
	Env         string
	HTTPAddr    string
	DatabaseURL string
	AutoMigrate bool
	LogLevel    slog.Level
	// CORSOrigins are browser origins allowed to call the API (Expo web in
	// development). Native apps do not need CORS.
	CORSOrigins []string
}

// Load reads configuration from the environment. DATABASE_URL is required;
// everything else has a development-friendly default.
func Load() (Config, error) {
	cfg := Config{
		Env:         getenv("APP_ENV", EnvDevelopment),
		HTTPAddr:    getenv("HTTP_ADDR", ":8080"),
		DatabaseURL: os.Getenv("DATABASE_URL"),
	}

	if cfg.Env != EnvDevelopment && cfg.Env != EnvProduction {
		return Config{}, fmt.Errorf("APP_ENV must be %q or %q, got %q", EnvDevelopment, EnvProduction, cfg.Env)
	}
	if cfg.DatabaseURL == "" {
		return Config{}, errors.New("DATABASE_URL is required")
	}

	autoMigrate, err := strconv.ParseBool(getenv("AUTO_MIGRATE", strconv.FormatBool(cfg.Env == EnvDevelopment)))
	if err != nil {
		return Config{}, fmt.Errorf("AUTO_MIGRATE: %w", err)
	}
	cfg.AutoMigrate = autoMigrate

	if err := cfg.LogLevel.UnmarshalText([]byte(getenv("LOG_LEVEL", "info"))); err != nil {
		return Config{}, fmt.Errorf("LOG_LEVEL: %w", err)
	}
	cfg.CORSOrigins = splitList(getenv("CORS_ALLOWED_ORIGINS", devOrigins(cfg.Env)))
	return cfg, nil
}

func devOrigins(env string) string {
	if env == EnvDevelopment {
		return "http://localhost:8081"
	}
	return ""
}

func splitList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

func getenv(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}
