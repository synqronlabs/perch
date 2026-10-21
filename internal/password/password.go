// Package password hashes and verifies account passwords with Argon2id.
package password

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

const (
	memory      = 64 * 1024
	iterations  = 3
	parallelism = 2
	saltLength  = 16
	keyLength   = 32
)

// ErrInvalidHash reports a password hash that is not in the PHC Argon2id
// format this package produces.
var ErrInvalidHash = errors.New("password: invalid argon2id hash")

// Hash returns a PHC-formatted Argon2id hash of password.
func Hash(password string) (string, error) {
	salt := make([]byte, saltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("password: generate salt: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, iterations, memory, parallelism, keyLength)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, memory, iterations, parallelism,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	), nil
}

// Verify reports whether password matches hash. It returns false without an
// error for a well-formed hash that does not match.
func Verify(hash, password string) (bool, error) {
	params, salt, want, err := parse(hash)
	if err != nil {
		return false, err
	}
	got := argon2.IDKey([]byte(password), salt, params.iterations, params.memory, params.parallelism, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

type params struct {
	memory      uint32
	iterations  uint32
	parallelism uint8
}

func parse(hash string) (params, []byte, []byte, error) {
	parts := strings.Split(hash, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return params{}, nil, nil, ErrInvalidHash
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return params{}, nil, nil, ErrInvalidHash
	}
	var p params
	var memory, iterations uint32
	var parallelism uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &iterations, &parallelism); err != nil {
		return params{}, nil, nil, ErrInvalidHash
	}
	if memory == 0 || iterations == 0 || parallelism == 0 {
		return params{}, nil, nil, ErrInvalidHash
	}
	p.memory = memory
	p.iterations = iterations
	p.parallelism = parallelism

	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) < 8 {
		return params{}, nil, nil, ErrInvalidHash
	}
	key, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(key) < 16 {
		return params{}, nil, nil, ErrInvalidHash
	}
	return p, salt, key, nil
}
