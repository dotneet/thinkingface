package store

import (
	"errors"
	"testing"
)

func TestIntegrationSSHKeys(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *Store) {
		f := newFixture(t, s)
		ctx := f.ctx

		if keys, err := s.ListSSHKeys(ctx, f.alice.ID); err != nil || len(keys) != 0 {
			t.Fatalf("ListSSHKeys on a fresh account = %v, %v; want an empty slice", keys, err)
		}

		laptop, err := s.CreateSSHKey(ctx, f.alice.ID, "laptop", "ssh-ed25519 AAAAlaptop", "SHA256:laptop")
		if err != nil {
			t.Fatalf("CreateSSHKey: %v", err)
		}
		if laptop.UserID != f.alice.ID || laptop.Title != "laptop" || laptop.LastUsedAt != nil {
			t.Fatalf("CreateSSHKey = %+v", laptop)
		}
		if _, err := s.CreateSSHKey(ctx, f.alice.ID, "desktop", "ssh-ed25519 AAAAdesktop", "SHA256:desktop"); err != nil {
			t.Fatalf("CreateSSHKey second: %v", err)
		}

		// A fingerprint is unique across the whole instance, not per user:
		// public key auth resolves an identity from the key alone.
		if _, err := s.CreateSSHKey(ctx, f.bob.ID, "stolen", "ssh-ed25519 AAAAlaptop", "SHA256:laptop"); !errors.Is(err, ErrConflict) {
			t.Fatalf("duplicate fingerprint err = %v, want ErrConflict", err)
		}

		keys, err := s.ListSSHKeys(ctx, f.alice.ID)
		if err != nil || len(keys) != 2 {
			t.Fatalf("ListSSHKeys = %v, %v; want 2 keys", keys, err)
		}
		if bobs, err := s.ListSSHKeys(ctx, f.bob.ID); err != nil || len(bobs) != 0 {
			t.Fatalf("ListSSHKeys for bob = %v, %v; want none", bobs, err)
		}

		user, key, err := s.LookupSSHKey(ctx, "SHA256:laptop")
		if err != nil {
			t.Fatalf("LookupSSHKey: %v", err)
		}
		if user.ID != f.alice.ID || key.ID != laptop.ID || key.PublicKey != "ssh-ed25519 AAAAlaptop" {
			t.Fatalf("LookupSSHKey = %+v / %+v", user, key)
		}
		if _, _, err := s.LookupSSHKey(ctx, "SHA256:nope"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("LookupSSHKey miss err = %v, want ErrNotFound", err)
		}

		if err := s.TouchSSHKey(ctx, laptop.ID); err != nil {
			t.Fatalf("TouchSSHKey: %v", err)
		}
		_, touched, err := s.LookupSSHKey(ctx, "SHA256:laptop")
		if err != nil {
			t.Fatalf("LookupSSHKey after touch: %v", err)
		}
		if touched.LastUsedAt == nil {
			t.Fatal("LastUsedAt is still nil after TouchSSHKey")
		}

		// Deleting is scoped to the owner: bob must not be able to remove
		// alice's key by guessing its id.
		if err := s.DeleteSSHKey(ctx, f.bob.ID, laptop.ID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("cross-user delete err = %v, want ErrNotFound", err)
		}
		if err := s.DeleteSSHKey(ctx, f.alice.ID, laptop.ID); err != nil {
			t.Fatalf("DeleteSSHKey: %v", err)
		}
		if err := s.DeleteSSHKey(ctx, f.alice.ID, laptop.ID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("second delete err = %v, want ErrNotFound", err)
		}
		if keys, err := s.ListSSHKeys(ctx, f.alice.ID); err != nil || len(keys) != 1 {
			t.Fatalf("ListSSHKeys after delete = %v, %v; want 1", keys, err)
		}
		// The fingerprint is free again once the key is gone.
		if _, err := s.CreateSSHKey(ctx, f.bob.ID, "laptop", "ssh-ed25519 AAAAlaptop", "SHA256:laptop"); err != nil {
			t.Fatalf("re-register a deleted fingerprint: %v", err)
		}
	})
}
