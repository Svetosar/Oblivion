// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"

	"golang.org/x/crypto/argon2"
)

// argon2id parameters (OWASP minimum profile: 19 MiB, t=2, p=1). Tuned for a
// 1 GB / 1 CPU host; a semaphore bounds concurrent hashes so a burst of logins
// cannot exhaust memory.
const (
	argonTime    = 2
	argonMemKiB  = 19 * 1024
	argonThreads = 1
	argonKeyLen  = 32
	saltLen      = 16

	minPasswordLen = 12
	maxPasswordLen = 256 // bytes; bounds hashing input
)

// Password hashing is the most expensive thing the server does. At most
// hashSlots run at once; up to maxHashWaiting more may wait; anything beyond
// that is refused immediately (errBusy -> HTTP 503) instead of queueing without
// bound. Waiting stops when the request is cancelled.
const (
	hashSlots      = 2
	maxHashWaiting = 8
)

var (
	hashSem     = make(chan struct{}, hashSlots)
	hashWaiting atomic.Int32
	errBusy     = errors.New("auth: too many concurrent password checks")
)

func acquireHash(ctx context.Context) (release func(), err error) {
	release = func() { <-hashSem }
	select {
	case hashSem <- struct{}{}:
		return release, nil
	default:
	}
	if hashWaiting.Add(1) > maxHashWaiting {
		hashWaiting.Add(-1)
		return nil, errBusy
	}
	defer hashWaiting.Add(-1)
	select {
	case hashSem <- struct{}{}:
		return release, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func hashPassword(ctx context.Context, pw string) (string, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	release, err := acquireHash(ctx)
	if err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(pw), salt, argonTime, argonMemKiB, argonThreads, argonKeyLen)
	release()
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, argonMemKiB, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

// verifyPassword checks pw against a PHC string in constant time.
func verifyPassword(ctx context.Context, pw, enc string) (bool, error) {
	parts := strings.Split(enc, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, errors.New("unsupported hash format")
	}
	var ver int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &ver); err != nil || ver != argon2.Version {
		return false, errors.New("unsupported argon2 version")
	}
	var m, t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil {
		return false, errors.New("bad argon2 params")
	}
	// Bound parameters read from the database so a tampered row cannot trigger a memory bomb.
	if m == 0 || m > 64*1024 || t == 0 || t > 10 || p == 0 || p > 4 {
		return false, errors.New("argon2 params out of range")
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) < 8 {
		return false, errors.New("bad salt")
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(want) < 16 || len(want) > 64 {
		return false, errors.New("bad hash")
	}
	release, err := acquireHash(ctx)
	if err != nil {
		return false, err
	}
	got := argon2.IDKey([]byte(pw), salt, t, m, p, uint32(len(want)))
	release()
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

func validatePassword(pw string) error {
	if len(pw) < minPasswordLen {
		return fmt.Errorf("password must be at least %d characters", minPasswordLen)
	}
	if len(pw) > maxPasswordLen {
		return fmt.Errorf("password must be at most %d bytes", maxPasswordLen)
	}
	return nil
}
