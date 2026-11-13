// Package config loads runtime configuration from environment variables.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"
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

	// JWTSecret signs access tokens. Required in production (32+ bytes).
	JWTSecret       []byte
	AccessTokenTTL  time.Duration
	RefreshTokenTTL time.Duration
	InvitationTTL   time.Duration
	// GoogleClientIDs are the OAuth client IDs (iOS, Android, web) whose ID
	// tokens are accepted. Empty disables Google sign-in.
	GoogleClientIDs []string
	// AuthRateLimit is requests per minute per IP on /v1/auth.
	AuthRateLimit int
}

// devJWTSecret is only used when APP_ENV=development and JWT_SECRET is unset.
const devJWTSecret = "development-only-secret-do-not-use-in-prod"

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

	secret := os.Getenv("JWT_SECRET")
	if secret == "" && cfg.Env == EnvDevelopment {
		secret = devJWTSecret
	}
	if len(secret) < 32 {
		return Config{}, errors.New("JWT_SECRET must be set to at least 32 characters (e.g. `openssl rand -base64 48`)")
	}
	cfg.JWTSecret = []byte(secret)

	durations := []struct {
		key, def string
		dst      *time.Duration
	}{
		{"ACCESS_TOKEN_TTL", "15m", &cfg.AccessTokenTTL},
		{"REFRESH_TOKEN_TTL", "720h", &cfg.RefreshTokenTTL},
		{"INVITATION_TTL", "168h", &cfg.InvitationTTL},
	}
	for _, d := range durations {
		v, err := time.ParseDuration(getenv(d.key, d.def))
		if err != nil || v <= 0 {
			return Config{}, fmt.Errorf("%s must be a positive duration like %s", d.key, d.def)
		}
		*d.dst = v
	}

	cfg.GoogleClientIDs = splitList(os.Getenv("GOOGLE_CLIENT_IDS"))

	limit, err := strconv.Atoi(getenv("AUTH_RATE_LIMIT", "20"))
	if err != nil || limit < 0 {
		return Config{}, errors.New("AUTH_RATE_LIMIT must be a non-negative integer")
	}
	cfg.AuthRateLimit = limit
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
