// Package auth contains the security-sensitive primitives of the
// controller: password hashing and the creation and checking of tokens.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

const (
	// MinPasswordLength is a floor, not a recommendation.
	MinPasswordLength = 8
	// MaxPasswordLength exists because bcrypt only looks at the first
	// 72 bytes; rather than silently ignoring the rest we reject it.
	MaxPasswordLength = 72
)

// Cost is the bcrypt work factor. 12 takes roughly a quarter of a
// second per login and only a few kilobytes of memory. It is a variable
// only so that tests can lower it; production code must not change it.
var Cost = 12

// ErrPasswordLength is returned for passwords that are too short or long.
var ErrPasswordLength = fmt.Errorf("password must be %d to %d bytes long", MinPasswordLength, MaxPasswordLength)

// HashPassword returns a salted bcrypt hash of password. The salt and the
// cost are part of the returned string.
func HashPassword(password string) (string, error) {
	if len(password) < MinPasswordLength || len(password) > MaxPasswordLength {
		return "", ErrPasswordLength
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), Cost)
	return string(hash), err
}

// CheckPassword reports whether password matches hash.
func CheckPassword(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

// NewSecret returns a random secret with 256 bits of entropy, in a form
// that is safe in URLs, cookies and YAML files.
func NewSecret() (string, error) {
	buf := make([]byte, 32)
	// crypto/rand, never math/rand: the latter is predictable.
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// HashSecret returns the hex-encoded SHA-256 of secret.
//
// Why not bcrypt as for passwords? A slow hash protects weak, guessable
// inputs. Our secrets are 256 random bits, so guessing is hopeless even
// with a fast hash, and agents authenticate on every heartbeat.
func HashSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// SecretMatches reports whether secret hashes to wantHash. The
// comparison takes the same time whether or not the values match, so
// response times reveal nothing about the stored hash.
func SecretMatches(secret, wantHash string) bool {
	return subtle.ConstantTimeCompare([]byte(HashSecret(secret)), []byte(wantHash)) == 1
}

// ErrMalformedToken is returned by ParseAgentToken.
var ErrMalformedToken = errors.New("malformed agent token")

// FormatAgentToken builds the token handed to an agent: "<id>.<secret>".
// The id lets the controller find the agent's row without having to
// compare the secret against every agent.
func FormatAgentToken(agentID int64, secret string) string {
	return strconv.FormatInt(agentID, 10) + "." + secret
}

// ParseAgentToken splits a token into agent id and secret.
func ParseAgentToken(token string) (agentID int64, secret string, err error) {
	idPart, secret, ok := strings.Cut(token, ".")
	if !ok || secret == "" {
		return 0, "", ErrMalformedToken
	}
	agentID, err = strconv.ParseInt(idPart, 10, 64)
	if err != nil || agentID <= 0 {
		return 0, "", ErrMalformedToken
	}
	return agentID, secret, nil
}
