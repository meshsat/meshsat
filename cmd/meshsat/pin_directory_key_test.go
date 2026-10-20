package main

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"meshsat/internal/database"
	"meshsat/internal/directory"
)

// TestPinDirectoryKey (r19 C7-R4): a pinned key is never fetched again nor
// replaced; a failed fetch stores nothing; a good one is pinned once.
func TestPinDirectoryKey(t *testing.T) {
	db, err := database.New(filepath.Join(t.TempDir(), "bridge.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	saved := fetchDirectoryKey
	defer func() { fetchDirectoryKey = saved }()
	var fetches, pinnedCalls int
	pinned := func() { pinnedCalls++ }

	// Nothing pinned, the fetch fails (a redirect refused, say): nothing
	// is stored, and the loop waits for the next try (ended here).
	ctx, cancel := context.WithCancel(context.Background())
	fetchDirectoryKey = func(context.Context, string) ([]byte, error) {
		fetches++
		cancel()
		return nil, errors.New("directory key: redirect to http://example.invalid refused")
	}
	pinDirectoryKey(ctx, db, "https://hub.example.invalid", "", pinned)
	if v, _ := db.GetSystemConfig(directory.RotationKeyConfig); v != "" || pinnedCalls != 0 || fetches != 1 {
		t.Fatalf("after a failed fetch: stored %q, pinned %d, fetches %d", v, pinnedCalls, fetches)
	}

	// A good answer is pinned.
	fetchDirectoryKey = func(context.Context, string) ([]byte, error) { fetches++; return []byte{0x30, 0x01}, nil }
	pinDirectoryKey(context.Background(), db, "https://hub.example.invalid", "", pinned)
	if v, _ := db.GetSystemConfig(directory.RotationKeyConfig); v != "3001" || pinnedCalls != 1 {
		t.Fatalf("stored %q, pinned %d", v, pinnedCalls)
	}

	// A key is pinned: the Hub is not asked, whatever it would answer.
	fetches = 0
	fetchDirectoryKey = func(context.Context, string) ([]byte, error) { fetches++; return []byte{0x30, 0x02}, nil }
	pinDirectoryKey(context.Background(), db, "https://hub.example.invalid", "", pinned)
	if v, _ := db.GetSystemConfig(directory.RotationKeyConfig); v != "3001" || fetches != 0 || pinnedCalls != 1 {
		t.Fatalf("a pinned key: stored %q, fetches %d, pinned %d", v, fetches, pinnedCalls)
	}
}
