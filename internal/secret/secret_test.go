package secret

import (
	"bytes"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTestCodec(t *testing.T) *Codec {
	t.Helper()
	key := make([]byte, keySize)
	for i := range key {
		key[i] = byte(i)
	}
	c, err := New(key)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func TestRoundTrip(t *testing.T) {
	c := newTestCodec(t)
	const plaintext = "abcdef0123456789abcdef0123456789"

	sealed, err := c.Encrypt("tmdb_api_key", plaintext)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	// The point of the whole package: what lands in the table is not the value.
	if strings.Contains(sealed, plaintext) {
		t.Fatal("the ciphertext contains the plaintext")
	}
	if !Encrypted(sealed) {
		t.Errorf("Encrypted(%q) = false", sealed)
	}

	opened, err := c.Decrypt("tmdb_api_key", sealed)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if opened != plaintext {
		t.Errorf("Decrypt = %q, want %q", opened, plaintext)
	}
}

// A wrong key must fail rather than return plausible garbage. It is what makes
// "this database was restored without its key" a state curator can report
// instead of a TMDB key made of noise.
func TestADifferentKeyCannotDecrypt(t *testing.T) {
	sealed, err := newTestCodec(t).Encrypt("qbit_pass", "hunter2hunter2")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	other := make([]byte, keySize)
	other[0] = 0xFF
	c, err := New(other)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := c.Decrypt("qbit_pass", sealed); err == nil {
		t.Fatal("decrypted with the wrong key")
	}
}

// The setting's key is the additional authenticated data, so a value cannot be
// lifted from one row into another inside the database.
func TestAValueCannotBeMovedBetweenSettings(t *testing.T) {
	c := newTestCodec(t)
	sealed, err := c.Encrypt("qbit_pass", "hunter2hunter2")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if _, err := c.Decrypt("tmdb_api_key", sealed); err == nil {
		t.Fatal("a qbit_pass ciphertext decrypted as tmdb_api_key")
	}
}

// Two seals of one value must differ, or the table leaks which settings share a
// value and when one stopped changing.
func TestTheSameValueSealsDifferentlyEachTime(t *testing.T) {
	c := newTestCodec(t)
	first, err := c.Encrypt("tmdb_api_key", "same value twice")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	second, err := c.Encrypt("tmdb_api_key", "same value twice")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if first == second {
		t.Fatal("two seals of one value are identical: the nonce is not doing its job")
	}
}

func TestRevealPassesPlaintextThroughAndDropsWhatItCannotRead(t *testing.T) {
	c := newTestCodec(t)
	sealed, err := c.Encrypt("tmdb_api_key", "a real key")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	values, unreadable := Reveal(c, map[string]string{
		"tmdb_api_key": sealed,
		"qbit_user":    "nethmin",
		"jellyfin_url": "http://127.0.0.1:8096",
		"qbit_pass":    Prefix + "not-actually-base64-$$$",
		"vpn_required": "true",
	})

	if values["tmdb_api_key"] != "a real key" {
		t.Errorf("tmdb_api_key = %q", values["tmdb_api_key"])
	}
	if values["qbit_user"] != "nethmin" {
		t.Errorf("plaintext did not pass through: %q", values["qbit_user"])
	}
	// Dropped, not passed through: a caller that received "enc.v1.…" as a
	// password would send it to qBittorrent.
	if _, ok := values["qbit_pass"]; ok {
		t.Error("an unreadable value was returned rather than dropped")
	}
	if len(unreadable) != 1 || unreadable[0] != "qbit_pass" {
		t.Errorf("unreadable = %v, want [qbit_pass]", unreadable)
	}
}

// A nil codec is the state a database restored without its key file is in, and
// it is a supported argument rather than a programming error.
func TestRevealWithNoKeyReportsEveryEncryptedValue(t *testing.T) {
	values, unreadable := Reveal(nil, map[string]string{
		"tmdb_api_key": Prefix + "AAAA",
		"qbit_user":    "nethmin",
	})
	if values["qbit_user"] != "nethmin" {
		t.Error("plaintext should still pass through without a key")
	}
	if len(unreadable) != 1 || unreadable[0] != "tmdb_api_key" {
		t.Errorf("unreadable = %v, want [tmdb_api_key]", unreadable)
	}
}

func TestKeyIsGeneratedOnceAtSixHundred(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "curator.key")

	first, err := Key("", path, true)
	if err != nil {
		t.Fatalf("Key: %v", err)
	}
	if len(first) != keySize {
		t.Fatalf("key is %d bytes, want %d", len(first), keySize)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("mode = %o, want 600", mode)
	}

	// A second call must read, never rewrite: a regenerated key orphans every
	// value written under the first one.
	second, err := Key("", path, true)
	if err != nil {
		t.Fatalf("Key again: %v", err)
	}
	if string(first) != string(second) {
		t.Fatal("the key changed between two reads")
	}
}

// The branch that matters: ciphertext is present and the key is not, so
// generating would silently orphan it. ErrNoKey is a state to report and start
// from, not a failure to exit on.
func TestKeyIsNotGeneratedWhenItWouldOrphanSecrets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "curator.key")

	_, err := Key("", path, false)
	if !errors.Is(err, ErrNoKey) {
		t.Fatalf("err = %v, want ErrNoKey", err)
	}
	if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
		t.Error("a key file was written when generating was refused")
	}
}

func TestAnyEncrypted(t *testing.T) {
	if AnyEncrypted(map[string]string{"qbit_user": "nethmin"}) {
		t.Error("plaintext reported as encrypted")
	}
	if !AnyEncrypted(map[string]string{"qbit_user": "nethmin", "qbit_pass": Prefix + "AAAA"}) {
		t.Error("ciphertext not reported")
	}
}

func TestAnInlineKeyIsRead(t *testing.T) {
	inline := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte("a"), keySize))
	key, err := Key(inline, "/nonexistent/curator.key", false)
	if err != nil {
		t.Fatalf("Key: %v", err)
	}
	if len(key) != keySize {
		t.Errorf("key is %d bytes", len(key))
	}
}

// The error must not carry the value: it is on its way to a log, and the value
// is a key.
func TestABadInlineKeyIsRefusedWithoutQuotingIt(t *testing.T) {
	const inline = "this-is-not-base64-!!!"
	_, err := Key(inline, "", false)
	if err == nil {
		t.Fatal("a malformed SECRET_KEY was accepted")
	}
	if strings.Contains(err.Error(), inline) {
		t.Errorf("the error carries the key: %v", err)
	}

	// Right encoding, wrong length is refused too — AES-256 or nothing.
	if _, err := Key("YWFhYWFh", "", false); err == nil {
		t.Fatal("a short key was accepted")
	}
}

// Derive is what T41's cookie is signed with, and the two properties it is
// depended on for are that it never changes and that it is not the key.
func TestDeriveIsStableAndNotTheKey(t *testing.T) {
	c := newTestCodec(t)

	first := c.Derive("curator session v1") // internal/api.SessionLabel; this package must not import it
	if len(first) != 32 {
		t.Errorf("Derive returned %d bytes, want 32", len(first))
	}

	// Deterministic, which is not an implementation detail: it is what makes a
	// cookie signed before a restart still verify after one, with no session
	// table anywhere.
	if !bytes.Equal(first, c.Derive("curator session v1")) {
		t.Error("Derive is not deterministic: every restart would log everybody out")
	}
	if bytes.Equal(first, c.Derive("something else")) {
		t.Error("two labels derived the same subkey")
	}

	// Not the key itself. A holder that could sign a cookie AND decrypt a
	// WireGuard config would be the reason not to hand it anything at all.
	key := make([]byte, keySize)
	for i := range key {
		key[i] = byte(i)
	}
	if bytes.Equal(first, key) {
		t.Error("Derive returned the key it was supposed to separate from")
	}

	// A different database's key gives a different subkey, which is what makes
	// a session unforgeable by anyone who has not read the key file.
	other, err := New(bytes.Repeat([]byte{7}, keySize))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if bytes.Equal(first, other.Derive("curator session v1")) {
		t.Error("two different keys derived the same subkey")
	}
}
