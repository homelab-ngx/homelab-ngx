// Package agefile encrypts small secrets to the host's own age key and
// reads them back with it (/var/lib/sops-age/keys.txt, a path kept from
// the hosts that came before the image): what is sealed here is unreadable
// in a backup and readable again on the host that holds the key.
package agefile

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"filippo.io/age"
)

// identities reads the age identities in keyFile.
func identities(keyFile string) ([]age.Identity, error) {
	if keyFile == "" {
		return nil, errors.New("no host age key file is configured")
	}
	f, err := os.Open(keyFile)
	if err != nil {
		return nil, fmt.Errorf("reading the host age key: %w", err)
	}
	defer f.Close()
	ids, err := age.ParseIdentities(f)
	if err != nil {
		return nil, fmt.Errorf("reading the host age key %s: %w", keyFile, err)
	}
	return ids, nil
}

// ParseIdentities parses an age key file's text (such as keys.txt) without
// echoing any secret material on error.
func ParseIdentities(text string) ([]age.Identity, error) {
	ids, err := age.ParseIdentities(strings.NewReader(text))
	if err != nil {
		return nil, fmt.Errorf("parsing age key: %w", err)
	}
	if len(ids) == 0 {
		return nil, errors.New("no age secret key found")
	}
	return ids, nil
}

// IsWrongKey reports whether err is because none of the supplied identities
// matched the encrypted file.
func IsWrongKey(err error) bool {
	var noMatch *age.NoIdentityMatchError
	return errors.As(err, &noMatch)
}

// OpenWith decrypts cipher using the supplied identities.
func OpenWith(ids []age.Identity, cipher []byte) ([]byte, error) {
	if len(ids) == 0 {
		return nil, errors.New("no age identity supplied")
	}
	r, err := age.Decrypt(bytes.NewReader(cipher), ids...)
	if err != nil {
		return nil, err
	}
	return io.ReadAll(r)
}

// recipients are the public halves of the X25519 identities.
func recipients(ids []age.Identity) ([]age.Recipient, error) {
	var out []age.Recipient
	for _, id := range ids {
		if x, ok := id.(*age.X25519Identity); ok {
			out = append(out, x.Recipient())
		}
	}
	if len(out) == 0 {
		return nil, errors.New("the host age key file has no X25519 identity to encrypt to")
	}
	return out, nil
}

// Seal encrypts plain to every X25519 identity in keyFile.
func Seal(keyFile string, plain []byte) ([]byte, error) {
	ids, err := identities(keyFile)
	if err != nil {
		return nil, err
	}
	recips, err := recipients(ids)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	w, err := age.Encrypt(&buf, recips...)
	if err != nil {
		return nil, err
	}
	if _, err := w.Write(plain); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Open decrypts what Seal wrote, trying every identity in keyFile.
func Open(keyFile string, cipher []byte) ([]byte, error) {
	ids, err := identities(keyFile)
	if err != nil {
		return nil, err
	}
	return OpenWith(ids, cipher)
}

// Reseal re-encrypts cipher from oldIDs to the identities in newKeyFile when
// newKeyFile alone cannot decrypt it. Already readable cipher returns
// (cipher, false, nil).
func Reseal(oldIDs []age.Identity, newKeyFile string, cipher []byte) ([]byte, bool, error) {
	if _, err := Open(newKeyFile, cipher); err == nil {
		return cipher, false, nil
	} else if !IsWrongKey(err) {
		return nil, false, err
	}
	plain, err := OpenWith(oldIDs, cipher)
	if err != nil {
		return nil, false, err
	}
	sealed, err := Seal(newKeyFile, plain)
	if err != nil {
		return nil, false, err
	}
	return sealed, true, nil
}

// EnsureKey makes keyFile with one new X25519 identity when it does not
// exist, so a fresh host can seal
// secrets. An existing file is left alone. It reports whether it made one.
func EnsureKey(keyFile string) (bool, error) {
	if _, err := os.Stat(keyFile); err == nil {
		return false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	id, err := age.GenerateX25519Identity()
	if err != nil {
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(keyFile), 0o700); err != nil {
		return false, err
	}
	content := fmt.Sprintf("# created: by homelab-daemon\n# public key: %s\n%s\n", id.Recipient(), id)
	// O_EXCL: a key that appeared meanwhile is never overwritten.
	f, err := os.OpenFile(keyFile, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return false, err
	}
	if _, err := f.WriteString(content); err != nil {
		f.Close()
		return false, err
	}
	return true, f.Close()
}
