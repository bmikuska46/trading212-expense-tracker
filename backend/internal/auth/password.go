// Package auth manages the single owner's credentials and sessions.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"

	"golang.org/x/crypto/argon2"
)

// Argon2id parameters from the OWASP password storage recommendations.
const (
	argonMemoryKiB = 19 * 1024
	argonTime      = 2
	argonThreads   = 1
	argonSaltBytes = 16
	argonKeyBytes  = 32
)

// Each hash allocates about 19 MiB, so bound concurrent hashing memory.
var hashSlots = make(chan struct{}, 2)

var ErrInvalidHash = errors.New("stored password hash is invalid")

func acquire(ctx context.Context) error {
	select {
	case hashSlots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func release() { <-hashSlots }

// HashPassword returns an Argon2id hash in the PHC string format.
func HashPassword(ctx context.Context, password string) (string, error) {
	salt := make([]byte, argonSaltBytes)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	if err := acquire(ctx); err != nil {
		return "", err
	}
	defer release()
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemoryKiB, argonThreads, argonKeyBytes)
	encoding := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, argonMemoryKiB, argonTime, argonThreads,
		encoding.EncodeToString(salt), encoding.EncodeToString(key)), nil
}

// VerifyPassword compares a password with a hash from HashPassword in constant time.
func VerifyPassword(ctx context.Context, encoded, password string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" || parts[2] != fmt.Sprintf("v=%d", argon2.Version) {
		return false, ErrInvalidHash
	}
	var memory, iterations uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &iterations, &threads); err != nil {
		return false, ErrInvalidHash
	}
	// Reject parameters that would let a modified row exhaust memory or CPU.
	if memory < 8*1024 || memory > 64*1024 || iterations < 1 || iterations > 10 || threads < 1 || threads > 4 {
		return false, ErrInvalidHash
	}
	salt, err := base64.RawStdEncoding.Strict().DecodeString(parts[4])
	if err != nil || len(salt) < 8 || len(salt) > 64 {
		return false, ErrInvalidHash
	}
	expected, err := base64.RawStdEncoding.Strict().DecodeString(parts[5])
	if err != nil || len(expected) < 16 || len(expected) > 64 {
		return false, ErrInvalidHash
	}
	if err := acquire(ctx); err != nil {
		return false, err
	}
	defer release()
	actual := argon2.IDKey([]byte(password), salt, iterations, memory, threads, uint32(len(expected)))
	return subtle.ConstantTimeCompare(actual, expected) == 1, nil
}

var dummyHash = sync.OnceValues(func() (string, error) {
	return HashPassword(context.Background(), "synthetic-password-for-timing")
})

// SpendVerification performs the same work as a real check so a missing
// account or a mismatched email does not answer faster than a wrong password.
func SpendVerification(ctx context.Context, password string) {
	if hash, err := dummyHash(); err == nil {
		_, _ = VerifyPassword(ctx, hash, password)
	}
}
