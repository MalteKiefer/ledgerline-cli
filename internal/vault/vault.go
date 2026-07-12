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

	"github.com/MalteKiefer/ledgerline-cli/internal/api"
	"github.com/MalteKiefer/ledgerline-cli/internal/crypto"
)

// ErrNotConfigured means the account has no vault yet (nothing to unlock).
var ErrNotConfigured = errors.New("vault: not configured for this account")

// ErrWrongPassphrase means the passphrase did not unwrap the vault key.
var ErrWrongPassphrase = errors.New("vault: wrong passphrase")

// Unlock derives the key-encryption key from the passphrase and the server's
// public KDF parameters, then unwraps and returns the 32-byte vault key.
func Unlock(ctx context.Context, client *api.Client, passphrase string) ([]byte, error) {
	status, err := client.Vault(ctx)
	if err != nil {
		return nil, err
	}
	if !status.Configured {
		return nil, ErrNotConfigured
	}

	salt, err := decodeB64(status.Salt)
	if err != nil {
		return nil, err
	}

	kek := crypto.DeriveKEK(passphrase, salt, status.KdfOps, status.KdfMem)
	vk, err := crypto.Open(crypto.Sealed{C: status.WrappedVaultKey, N: status.WrapNonce}, kek)
	if err != nil {
		return nil, ErrWrongPassphrase
	}
	return vk, nil
}

// RecoverWithCode unlocks the vault with the high-entropy recovery code instead
// of the passphrase (spaces are ignored), matching vault.js recover().
func RecoverWithCode(ctx context.Context, client *api.Client, recoveryCodeHex string) ([]byte, error) {
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
		return nil, errors.New("vault: wrong recovery code")
	}
	return vk, nil
}
