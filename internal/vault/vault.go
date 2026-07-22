// Package vault unlocks a user's zero-knowledge vault key from a passphrase,
// mirroring resources/js/vault.js. The vault key (VK) is what encrypts all
// content; it is derived entirely client-side and never sent to the server.
//
// Authentication (the Sanctum bearer) and vault unlock are separate concerns:
// the bearer proves identity, the passphrase unlocks the VK. Callers hold the VK
// only in memory for the duration of a command.
package vault

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/MalteKiefer/ledgerline-cli/internal/api"
	"github.com/MalteKiefer/ledgerline-cli/internal/crypto"
)

// failFloor is the minimum wall-clock duration a failed secret check takes,
// measured on the monotonic clock. It gives wrong-passphrase / wrong-recovery
// failures a uniform floor (no timing oracle, §28) and a brute-force speed bump
// (§23). Argon2id already dominates a normal unlock, so this only actually delays
// the fast paths (e.g. recovery, which uses a cheap KDF).
const failFloor = 750 * time.Millisecond

// padFailure blocks until at least failFloor has elapsed since start, honouring
// context cancellation (a cancelled context returns promptly).
func padFailure(ctx context.Context, start time.Time) {
	remaining := failFloor - time.Since(start)
	if remaining <= 0 {
		return
	}
	t := time.NewTimer(remaining)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

// ErrNotConfigured means the account has no vault yet (nothing to unlock).
var ErrNotConfigured = errors.New("vault: not configured for this account")

// ErrWrongPassphrase means the passphrase did not unwrap the vault key.
var ErrWrongPassphrase = errors.New("vault: wrong passphrase")

// Unlock derives the key-encryption key from the passphrase and the server's
// public KDF parameters, then unwraps and returns the 32-byte vault key.
func Unlock(ctx context.Context, client *api.Client, passphrase string) ([]byte, error) {
	start := time.Now()
	status, err := client.Vault(ctx)
	if err != nil {
		return nil, err
	}
	if !status.Configured {
		return nil, ErrNotConfigured
	}

	if err := validateKDF(status.KdfOps, status.KdfMem); err != nil {
		return nil, err
	}
	// Fail closed if the host/cgroup cannot hold the Argon2id derivation, rather
	// than being OOM-killed mid-derivation on a memory-constrained host (§4a).
	if err := checkMemoryFor(status.KdfMem); err != nil {
		return nil, err
	}
	salt, err := decodeB64(status.Salt)
	if err != nil {
		return nil, err
	}

	kek := crypto.DeriveKEK(passphrase, salt, status.KdfOps, status.KdfMem)
	vk, err := crypto.Open(crypto.Sealed{C: status.WrappedVaultKey, N: status.WrapNonce}, kek)
	if err != nil {
		padFailure(ctx, start)
		return nil, ErrWrongPassphrase
	}
	return vk, nil
}

// KDF parameter bounds. The server supplies opslimit/memlimit; clamp them to a
// sane range so a hostile server can neither weaken the derivation (tiny params)
// nor exhaust client memory/CPU before the passphrase is even checked (huge
// params). libsodium SENSITIVE/MODERATE (ops 4, mem 256 MiB) sits well inside.
const (
	minKdfOps = 1
	maxKdfOps = 16
	minKdfMem = 8 * 1024 * 1024        // 8 MiB
	maxKdfMem = 2 * 1024 * 1024 * 1024 // 2 GiB
)

// validateKDF rejects out-of-range server-supplied Argon2id parameters.
func validateKDF(ops, memBytes uint64) error {
	if ops < minKdfOps || ops > maxKdfOps {
		return fmt.Errorf("vault: server KDF opslimit %d out of accepted range [%d,%d]", ops, minKdfOps, maxKdfOps)
	}
	if memBytes < minKdfMem || memBytes > maxKdfMem {
		return fmt.Errorf("vault: server KDF memlimit %d out of accepted range [%d,%d]", memBytes, minKdfMem, maxKdfMem)
	}
	return nil
}

// RecoverWithCode unlocks the vault with the high-entropy recovery code instead
// of the passphrase (spaces are ignored), matching vault.js recover().
func RecoverWithCode(ctx context.Context, client *api.Client, recoveryCodeHex string) ([]byte, error) {
	start := time.Now()
	status, err := client.Vault(ctx)
	if err != nil {
		return nil, err
	}
	if !status.Configured || !status.HasRecovery {
		return nil, errors.New("vault: no recovery configured")
	}

	recoveryBytes, err := decodeHexNoSpaces(recoveryCodeHex)
	if err != nil {
		return nil, err
	}
	recoveryKey := crypto.GenericHashKey(recoveryBytes)
	vk, err := crypto.Open(crypto.Sealed{C: status.WrappedVaultKeyRecovery, N: status.RecoveryNonce}, recoveryKey)
	if err != nil {
		// Cheap KDF here, so the floor is what actually rate-limits a brute force
		// and removes the timing oracle (§23/§28).
		padFailure(ctx, start)
		return nil, errors.New("vault: wrong recovery code")
	}
	return vk, nil
}
