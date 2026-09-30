package handlers

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/reynerpantou/libra/internal/database"
)

// TestOwnerClaim needs an empty Postgres database:
// LIBRA_TEST_SETUP_DATABASE_URL=postgres://.../libra_setup_test
func TestOwnerClaim(t *testing.T) {
	dsn := os.Getenv("LIBRA_TEST_SETUP_DATABASE_URL")
	if dsn == "" {
		t.Skip("LIBRA_TEST_SETUP_DATABASE_URL not set")
	}
	ctx := context.Background()
	db, err := database.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.Migrate(db); err != nil {
		t.Fatal(err)
	}
	s := &Server{DB: db}

	// New install: a link is issued.
	link, err := NewSetupLink(ctx, db, "http://x")
	if err != nil || !strings.HasPrefix(link, "http://x/setup#") {
		t.Fatalf("link %q err %v", link, err)
	}
	// Unverified email can't claim.
	if _, err := s.claimOwner(ctx, "google", "sub-1", "a@example.com", false, "A"); !errors.Is(err, errNotInvited) {
		t.Fatalf("unverified claim: %v", err)
	}
	uid, err := s.claimOwner(ctx, "google", "sub-1", "Reyner.Pantou+x@Example.com", true, "Reyner")
	if err != nil {
		t.Fatal(err)
	}
	var username, email, role, display string
	var owner bool
	if err := db.QueryRow(`SELECT username, email, role, is_owner, display_name FROM users WHERE id = $1`, uid).Scan(&username, &email, &role, &owner, &display); err != nil {
		t.Fatal(err)
	}
	if username != "reyner.pantou" || role != "admin" || !owner || display != "Reyner" {
		t.Errorf("owner = %s %s %s %v %s", username, email, role, owner, display)
	}
	// Second claim loses, and no more links are issued.
	if _, err := s.claimOwner(ctx, "google", "sub-2", "b@example.com", true, "B"); !errors.Is(err, errSetupDone) {
		t.Errorf("second claim: %v", err)
	}
	if link, _ := NewSetupLink(ctx, db, "http://x"); link != "" {
		t.Errorf("link after claim: %q", link)
	}

	// An owner left without email or identity (older installs) can be
	// claimed again, keeping the same account.
	if _, err := db.Exec(`UPDATE users SET email = NULL WHERE id = $1`, uid); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM user_identities`); err != nil {
		t.Fatal(err)
	}
	if link, _ := NewSetupLink(ctx, db, "http://x"); link == "" {
		t.Fatal("expected a link for an owner who can't sign in")
	}
	again, err := s.claimOwner(ctx, "google", "sub-3", "c@example.com", true, "")
	if err != nil || again != uid {
		t.Fatalf("reclaim: uid %d err %v", again, err)
	}
}
