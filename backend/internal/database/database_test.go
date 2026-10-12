package database

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEncryptedRuntimeAndReopenedConnections(t *testing.T) {
	directory := t.TempDir()
	keyFile := filepath.Join(directory, "key")
	key := strings.Repeat("ab", 32)
	if err := os.WriteFile(keyFile, []byte(key+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "data", "budget.db")
	ctx := context.Background()
	store, err := Open(ctx, path, keyFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(store.CipherVersion, "4.") {
		t.Fatalf("expected SQLCipher 4, got %q", store.CipherVersion)
	}
	created, err := store.Queries.GetRuntime(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Force replacement connections to prove each connection receives the key.
	store.DB.SetMaxIdleConns(0)
	for range 3 {
		actual, err := store.Queries.GetRuntime(ctx)
		if err != nil || actual != created {
			t.Fatalf("replacement connection: %q, %v", actual, err)
		}
	}
	if err := store.DB.Close(); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(content) == 0 || bytes.HasPrefix(content, []byte("SQLite format 3")) || bytes.Contains(content, []byte("runtime_meta")) {
		t.Fatal("database is empty or exposes plaintext schema")
	}
	reopened, err := Open(ctx, path, keyFile)
	if err != nil {
		t.Fatal(err)
	}
	actual, err := reopened.Queries.GetRuntime(ctx)
	if err != nil || actual != created {
		t.Fatalf("restart lost runtime metadata: %q, %v", actual, err)
	}
	reopened.DB.Close()
	wrongKey := strings.Repeat("cd", 32)
	if err := os.WriteFile(keyFile, []byte(wrongKey), 0600); err != nil {
		t.Fatal(err)
	}
	if wrong, err := Open(ctx, path, keyFile); err == nil {
		wrong.DB.Close()
		t.Fatal("incorrect key accepted")
	} else if message := err.Error(); strings.Contains(message, key) || strings.Contains(message, wrongKey) || strings.Contains(message, path) {
		t.Fatal("key or database path leaked in error")
	} else if !strings.Contains(message, "cannot unlock database") {
		t.Fatalf("wrong key must report an unlock failure, got %q", message)
	}
}

func TestRejectInvalidKeysBeforeCreatingDatabase(t *testing.T) {
	for _, test := range []struct {
		name, key string
		mode      os.FileMode
	}{
		{"empty", "", 0600}, {"short", "abcdef", 0600}, {"not hex", strings.Repeat("z", 64), 0600},
		{"oversized", strings.Repeat("a", 67), 0600}, {"public", strings.Repeat("a", 64), 0644},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			keyFile := filepath.Join(directory, "key")
			if err := os.WriteFile(keyFile, []byte(test.key), test.mode); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(directory, "data", "budget.db")
			if store, err := Open(context.Background(), path, keyFile); err == nil {
				store.DB.Close()
				t.Fatal("invalid key accepted")
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatal("invalid key created a database")
			}
		})
	}
}
