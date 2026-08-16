package api

import (
	"context"
	"strconv"
)

// EncryptFile public-key encrypts a file to one of the user's own keys (from
// the keyring; see Keyring) plus any chosen recipients, always including the
// own key so it stays decryptable. PGP -> name.gpg, S/MIME -> name.p7m, saved
// beside the original as a new FileEntry. recipientIDs must be the same key
// type (pgp/smime) as keyID.
// POST /files/entries/{id}/encrypt.
func (c *Client) EncryptFile(ctx context.Context, id, keyID int64, recipientIDs []int64) (FileEntry, error) {
	body := map[string]any{"key_id": keyID}
	if len(recipientIDs) > 0 {
		body["recipient_ids"] = recipientIDs
	}
	var resp struct {
		File FileEntry `json:"file"`
	}
	path := "/api/v1/files/entries/" + strconv.FormatInt(id, 10) + "/encrypt"
	if err := c.request(ctx, "POST", path, body, &resp); err != nil {
		return FileEntry{}, err
	}
	return resp.File, nil
}

// DecryptFile decrypts an encrypted file (name.gpg / name.p7m) back to
// plaintext with one of the user's own keys, saved beside the ciphertext
// (extension stripped) as a new FileEntry.
// POST /files/entries/{id}/decrypt.
func (c *Client) DecryptFile(ctx context.Context, id, keyID int64, passphrase *string) (FileEntry, error) {
	body := map[string]any{"key_id": keyID}
	if passphrase != nil {
		body["passphrase"] = *passphrase
	}
	var resp struct {
		File FileEntry `json:"file"`
	}
	path := "/api/v1/files/entries/" + strconv.FormatInt(id, 10) + "/decrypt"
	if err := c.request(ctx, "POST", path, body, &resp); err != nil {
		return FileEntry{}, err
	}
	return resp.File, nil
}

// EncryptFolder bundles a folder's subtree into a tar.gz and encrypts it to
// the chosen key(s), producing folder.tar.gz.gpg (or .p7m) saved beside the
// folder as a new FileEntry. Same caps as FilesZip.
// POST /files/folders/{id}/encrypt.
func (c *Client) EncryptFolder(ctx context.Context, folderID, keyID int64, recipientIDs []int64) (FileEntry, error) {
	body := map[string]any{"key_id": keyID}
	if len(recipientIDs) > 0 {
		body["recipient_ids"] = recipientIDs
	}
	var resp struct {
		File FileEntry `json:"file"`
	}
	path := "/api/v1/files/folders/" + strconv.FormatInt(folderID, 10) + "/encrypt"
	if err := c.request(ctx, "POST", path, body, &resp); err != nil {
		return FileEntry{}, err
	}
	return resp.File, nil
}

// CryptoKey is one of the user's own PGP/S-MIME keys, or a saved recipient's
// public key/certificate (public info only; private material is never
// returned).
type CryptoKey struct {
	ID          int64   `json:"id"`
	Type        string  `json:"type"` // pgp, smime
	Label       string  `json:"label"`
	Fingerprint *string `json:"fingerprint"`
	HasPrivate  bool    `json:"has_private"` // own keys only
	IsOwn       bool    `json:"is_own"`      // own keys only
}

// Keyring returns the encryption keyring: the user's own keys (usable as
// EncryptFile/EncryptFolder's keyID, or DecryptFile's keyID) plus saved
// recipients (usable as recipientIDs). GET /crypto/keyring.
func (c *Client) Keyring(ctx context.Context) (keys, recipients []CryptoKey, err error) {
	var resp struct {
		Keys       []CryptoKey `json:"keys"`
		Recipients []CryptoKey `json:"recipients"`
	}
	if err = c.request(ctx, "GET", "/api/v1/crypto/keyring", nil, &resp); err != nil {
		return nil, nil, err
	}
	return resp.Keys, resp.Recipients, nil
}
