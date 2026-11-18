package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Argon2id parameters for a new hash: 64 MiB, two passes, one lane. A
// stored hash carries its own, so these can be raised later without
// breaking older ones.
const (
	argonMemory  = 64 * 1024
	argonTime    = 2
	argonThreads = 1
	argonKeyLen  = 32
	argonSaltLen = 16
)

// MinPasswordLen and MaxPasswordLen bound a new password, in bytes.
const (
	MinPasswordLen = 10
	MaxPasswordLen = 256
)

var errBadHash = errors.New("auth: malformed password hash")

// hashPassword returns pw's argon2id hash in the PHC string format.
func hashPassword(pw string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(pw), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	b64 := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads, b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

// checkPassword reports whether pw matches the PHC string hash.
func checkPassword(hash, pw string) (bool, error) {
	parts := strings.Split(hash, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, errBadHash
	}
	var v int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &v); err != nil || v != argon2.Version {
		return false, errBadHash
	}
	var m, t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil || m == 0 || t == 0 || p == 0 {
		return false, errBadHash
	}
	b64 := base64.RawStdEncoding
	salt, err := b64.DecodeString(parts[4])
	if err != nil {
		return false, errBadHash
	}
	want, err := b64.DecodeString(parts[5])
	if err != nil || len(want) == 0 {
		return false, errBadHash
	}
	got := argon2.IDKey([]byte(pw), salt, t, m, p, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// validPassword is nil for a password that may be set.
func validPassword(pw string) error {
	switch {
	case len(pw) < MinPasswordLen:
		return fmt.Errorf("%w: at least %d characters", ErrWeakPassword, MinPasswordLen)
	case len(pw) > MaxPasswordLen:
		return fmt.Errorf("%w: at most %d characters", ErrWeakPassword, MaxPasswordLen)
	}
	return nil
}
