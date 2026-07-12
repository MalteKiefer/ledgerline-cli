package api

import "context"

// VaultStatus is the server's public vault KDF parameters and wrapped keys, as
// returned by GET /api/v1/vault. Everything here is public by design; the
// passphrase and vault key never leave the client.
type VaultStatus struct {
	Configured              bool   `json:"configured"`
	Salt                    string `json:"salt"`
	KdfOps                  uint64 `json:"kdf_ops"`
	KdfMem                  uint64 `json:"kdf_mem"`
	WrappedVaultKey         string `json:"wrapped_vault_key"`
	WrapNonce               string `json:"wrap_nonce"`
	HasRecovery             bool   `json:"has_recovery"`
	WrappedVaultKeyRecovery string `json:"wrapped_vault_key_recovery"`
	RecoveryNonce           string `json:"recovery_nonce"`
}

// Vault fetches the vault KDF parameters and wrapped keys for the authenticated
// user.
func (c *Client) Vault(ctx context.Context) (VaultStatus, error) {
	var out VaultStatus
	if err := c.request(ctx, "GET", "/api/v1/vault", nil, &out); err != nil {
		return VaultStatus{}, err
	}
	return out, nil
}
