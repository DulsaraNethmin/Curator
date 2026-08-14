// Package secret encrypts the settings curator has to be able to read back.
//
// Phase 7 lets settings be written from the UI, which puts a TMDB key, a
// qBittorrent password, a Jellyfin key and — the one that forced this — a
// WireGuard private key into the database (docs/decisions.md D28). They are
// encrypted there with AES-256-GCM under a key held beside the database.
//
// **Be exact about what that buys.** The key file sits in the same directory as
// curator.db, so anything that copies the volume copies both. What it defends
// against is narrower and entirely real: a curator.db pasted into an issue for
// help, a backup that globs *.db, a future handler that returns a settings row
// by accident, and anyone who can read the file without owning the process. It
// is not protection against somebody who has the machine — they can read the
// key, and the plaintext is in this process's memory regardless.
//
// **Encrypt what must be used; hash what is only ever compared.** Everything
// here is reversible because curator has to bring up a tunnel with the key it
// stored. A password is never in this package: it is bcrypt, one-way, because
// curator only ever has to compare one.
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Prefix marks a stored value as ciphertext. It carries a version because the
// day the scheme changes, the old values are still in somebody's database and
// have to be readable long enough to be rewritten.
const Prefix = "enc.v1."

// keySize is AES-256. Not configurable: a key length that can be got wrong is a
// key length that will be.
const keySize = 32

// ErrNoKey reports that secrets are stored but the key that would read them is
// not there. It is a sentinel because the caller's response is specific and not
// fatal: start anyway, say so, and report those settings as unreadable rather
// than as unset (docs/tasks/T39-settings-store.md).
var ErrNoKey = errors.New("secrets are stored but the key is missing")

// Codec encrypts and decrypts one database's settings.
type Codec struct{ aead cipher.AEAD }

// New builds a codec from a raw 32-byte key.
func New(key []byte) (*Codec, error) {
	if len(key) != keySize {
		return nil, fmt.Errorf("secret key is %d bytes, want %d", len(key), keySize)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("secret key: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("secret key: %w", err)
	}
	return &Codec{aead: aead}, nil
}

// Encrypt seals plaintext for the setting called name.
//
// The name is the additional authenticated data, so a ciphertext cannot be
// moved from one row to another inside the database: lifting the value of
// `qbit_pass` into `tmdb_api_key` produces a decryption failure rather than a
// working TMDB key nobody chose.
func (c *Codec) Encrypt(name, plaintext string) (string, error) {
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("encrypt %s: %w", name, err)
	}
	sealed := c.aead.Seal(nonce, nonce, []byte(plaintext), []byte(name))
	return Prefix + base64.RawStdEncoding.EncodeToString(sealed), nil
}

// Decrypt opens a value written by Encrypt under the same name.
//
// A wrong key fails here rather than returning plausible garbage — GCM's
// authentication tag is what makes "this database was restored without its key"
// a detectable state instead of a TMDB key made of noise.
func (c *Codec) Decrypt(name, stored string) (string, error) {
	if !Encrypted(stored) {
		return "", fmt.Errorf("decrypt %s: not encrypted", name)
	}
	raw, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(stored, Prefix))
	if err != nil {
		return "", fmt.Errorf("decrypt %s: %w", name, err)
	}
	if len(raw) < c.aead.NonceSize() {
		return "", fmt.Errorf("decrypt %s: too short to be a sealed value", name)
	}
	nonce, body := raw[:c.aead.NonceSize()], raw[c.aead.NonceSize():]
	opened, err := c.aead.Open(nil, nonce, body, []byte(name))
	if err != nil {
		return "", fmt.Errorf("decrypt %s: %w", name, err)
	}
	return string(opened), nil
}

// Encrypted reports whether a stored value is one of ours.
func Encrypted(value string) bool { return strings.HasPrefix(value, Prefix) }

// AnyEncrypted reports whether a settings map holds anything that needs a key.
// It is what decides whether a missing key file may be generated: a key made up
// on top of secrets that were already there would turn "I restored the database
// without its key" from recoverable into invisible.
func AnyEncrypted(raw map[string]string) bool {
	for _, v := range raw {
		if Encrypted(v) {
			return true
		}
	}
	return false
}

// Reveal decrypts every encrypted value in raw and passes the rest through.
//
// A value that cannot be read — wrong key, no key at all, a truncated row — is
// DROPPED from the result and its key returned in unreadable. Dropping is the
// point: a caller that received "enc.v1.…" as a TMDB API key would send it to
// TMDB. The setting reports itself unreadable and asks to be entered again,
// which is recoverable; a request authenticated with ciphertext is not.
//
// A nil codec is a supported argument, not a programming error: it is exactly
// the state a database restored without its key file is in.
func Reveal(c *Codec, raw map[string]string) (values map[string]string, unreadable []string) {
	values = make(map[string]string, len(raw))
	for key, stored := range raw {
		if !Encrypted(stored) {
			values[key] = stored
			continue
		}
		if c == nil {
			unreadable = append(unreadable, key)
			continue
		}
		plain, err := c.Decrypt(key, stored)
		if err != nil {
			unreadable = append(unreadable, key)
			continue
		}
		values[key] = plain
	}
	return values, unreadable
}

// Open builds the codec for a database, finding or generating its key.
//
// It returns a nil *Codec together with ErrNoKey when there is a key to find
// and it is not there — which is a state to report and start from, not one to
// fail on, so the caller can say which settings are unreadable instead of
// refusing to serve a library over a TMDB key.
func Open(inline, path string, generate bool) (*Codec, error) {
	key, err := Key(inline, path, generate)
	if err != nil {
		return nil, err
	}
	return New(key)
}

// Key returns the encryption key, from inline text or from a file, generating
// one only when generate is true.
//
// inline is SECRET_KEY and path is SECRET_KEY_FILE — the same pair of shapes
// VPN_CONFIG and VPN_CONFIG_FILE already have, because a provider hands you a
// file and a container hands you a variable.
//
// generate is the caller's answer to "is there anything here that a new key
// would orphan": true only when the database holds no ciphertext at all.
func Key(inline, path string, generate bool) ([]byte, error) {
	if trimmed := strings.TrimSpace(inline); trimmed != "" {
		key, err := decodeKey(trimmed)
		if err != nil {
			return nil, fmt.Errorf("SECRET_KEY: %w", err)
		}
		return key, nil
	}
	if path == "" {
		return nil, ErrNoKey
	}

	body, err := os.ReadFile(path)
	switch {
	case err == nil:
		key, decodeErr := decodeKey(strings.TrimSpace(string(body)))
		if decodeErr != nil {
			return nil, fmt.Errorf("SECRET_KEY_FILE %s: %w", path, decodeErr)
		}
		return key, nil
	case !errors.Is(err, os.ErrNotExist):
		return nil, fmt.Errorf("SECRET_KEY_FILE %s: %w", path, err)
	case !generate:
		return nil, ErrNoKey
	}
	return generateKey(path)
}

// generateKey writes a new key at path, 0600, and returns it.
func generateKey(path string) ([]byte, error) {
	key := make([]byte, keySize)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate secret key: %w", err)
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("generate secret key: %w", err)
		}
	}

	// O_EXCL, so two curator processes starting against one directory cannot
	// both believe they made the key — the loser reads what the winner wrote
	// instead of encrypting everything under a key that is about to be
	// overwritten.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return Key("", path, false)
		}
		return nil, fmt.Errorf("generate secret key: %w", err)
	}
	if _, err := f.WriteString(base64.StdEncoding.EncodeToString(key) + "\n"); err != nil {
		f.Close()
		return nil, fmt.Errorf("generate secret key: %w", err)
	}
	if err := f.Close(); err != nil {
		return nil, fmt.Errorf("generate secret key: %w", err)
	}
	return key, nil
}

func decodeKey(encoded string) ([]byte, error) {
	key, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		// Deliberately does not quote the value: an error naming a key is an
		// error carrying a key, and this one is on its way to a log.
		return nil, errors.New("not base64")
	}
	if len(key) != keySize {
		return nil, fmt.Errorf("decodes to %d bytes, want %d", len(key), keySize)
	}
	return key, nil
}
