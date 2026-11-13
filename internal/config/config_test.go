package config

import (
	"log/slog"
	"testing"
)

func TestLoadDefaults(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/test")
	t.Setenv("APP_ENV", "")
	t.Setenv("AUTO_MIGRATE", "")
	t.Setenv("LOG_LEVEL", "")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Env != EnvDevelopment || cfg.HTTPAddr != ":8080" || !cfg.AutoMigrate || cfg.LogLevel != slog.LevelInfo {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
}

func TestLoadProductionDisablesAutoMigrate(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/test")
	t.Setenv("APP_ENV", "production")
	t.Setenv("JWT_SECRET", "0123456789abcdef0123456789abcdef")
	t.Setenv("AUTO_MIGRATE", "")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AutoMigrate {
		t.Fatal("AUTO_MIGRATE should default to false in production")
	}
}

func TestLoadRequiresDatabaseURL(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	if _, err := Load(); err == nil {
		t.Fatal("expected error when DATABASE_URL is missing")
	}
}

func TestLoadRejectsUnknownEnv(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/test")
	t.Setenv("APP_ENV", "staging-ish")
	if _, err := Load(); err == nil {
		t.Fatal("expected error for unknown APP_ENV")
	}
}

func TestLoadProductionRequiresJWTSecret(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/test")
	t.Setenv("APP_ENV", "production")
	t.Setenv("JWT_SECRET", "")
	if _, err := Load(); err == nil {
		t.Fatal("expected error without JWT_SECRET in production")
	}
	t.Setenv("JWT_SECRET", "too-short")
	if _, err := Load(); err == nil {
		t.Fatal("expected error for a short JWT_SECRET")
	}
}

func TestLoadAuthSettings(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/test")
	t.Setenv("APP_ENV", "")
	t.Setenv("GOOGLE_CLIENT_IDS", "ios.apps.googleusercontent.com, web.apps.googleusercontent.com")
	t.Setenv("ACCESS_TOKEN_TTL", "")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.GoogleClientIDs) != 2 || cfg.GoogleClientIDs[1] != "web.apps.googleusercontent.com" {
		t.Fatalf("client IDs: %v", cfg.GoogleClientIDs)
	}
	if cfg.AccessTokenTTL.Minutes() != 15 || cfg.RefreshTokenTTL.Hours() != 720 {
		t.Fatalf("ttls: %v %v", cfg.AccessTokenTTL, cfg.RefreshTokenTTL)
	}
	t.Setenv("ACCESS_TOKEN_TTL", "soon")
	if _, err := Load(); err == nil {
		t.Fatal("expected error for bad duration")
	}
}
