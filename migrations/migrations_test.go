package migrations

import (
	"context"
	"os"
	"testing"
)

// TestUp runs every migration against a real database. It is skipped unless
// TEST_DATABASE_URL is set (CI sets it; locally use `make test-db`).
func TestUp(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	if err := Up(ctx, url); err != nil {
		t.Fatal(err)
	}
	// Running twice must be a no-op.
	if err := Up(ctx, url); err != nil {
		t.Fatal(err)
	}
}
