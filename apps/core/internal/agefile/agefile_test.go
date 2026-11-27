package agefile

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"
)

func keyFile(t *testing.T, ids ...*age.X25519Identity) string {
	t.Helper()
	var content string
	for _, id := range ids {
		content += "# public key: " + id.Recipient().String() + "\n" + id.String() + "\n"
	}
	path := filepath.Join(t.TempDir(), "keys.txt")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func newID(t *testing.T) *age.X25519Identity {
	t.Helper()
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestSealAndOpenRoundTrip(t *testing.T) {
	t.Parallel()
	host := newID(t)
	key := keyFile(t, host)
	sealed, err := Seal(key, []byte("hunter2"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, []byte("hunter2")) {
		t.Fatal("the sealed bytes hold the plain text")
	}
	plain, err := Open(key, sealed)
	if err != nil || string(plain) != "hunter2" {
		t.Fatalf("Open = %q, %v", plain, err)
	}
	// Every identity in the file is tried.
	if plain, err := Open(keyFile(t, newID(t), host), sealed); err != nil || string(plain) != "hunter2" {
		t.Errorf("Open with two identities = %q, %v", plain, err)
	}
}

func TestOpenReadsWhatPlainAgeWrote(t *testing.T) {
	t.Parallel()
	// The format is age's own: a file written straight with the library, as
	// the stacks package did before this package existed, still opens.
	host := newID(t)
	var buf bytes.Buffer
	w, err := age.Encrypt(&buf, host.Recipient())
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.Write([]byte(`{"version":1}`))
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	plain, err := Open(keyFile(t, host), buf.Bytes())
	if err != nil || string(plain) != `{"version":1}` {
		t.Fatalf("Open = %q, %v", plain, err)
	}
}

func TestAWrongOrMissingKeyReadsNothing(t *testing.T) {
	t.Parallel()
	sealed, err := Seal(keyFile(t, newID(t)), []byte("hunter2"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Open(keyFile(t, newID(t)), sealed); err == nil {
		t.Error("opened with a key it was not sealed to")
	}
	if _, err := Open("", sealed); err == nil {
		t.Error("opened with no key file")
	}
	if _, err := Seal(filepath.Join(t.TempDir(), "absent"), []byte("x")); err == nil {
		t.Error("sealed with a key file that does not exist")
	}
	if _, err := Seal(keyFile(t), []byte("x")); err == nil {
		t.Error("sealed with a key file that has no identity")
	}
}

func TestEnsureKey(t *testing.T) {
	t.Parallel()
	p := filepath.Join(t.TempDir(), "sops-age", "keys.txt")
	made, err := EnsureKey(p)
	if err != nil || !made {
		t.Fatalf("made=%v err=%v", made, err)
	}
	sealed, err := Seal(p, []byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	if plain, err := Open(p, sealed); err != nil || string(plain) != "x" {
		t.Fatalf("round trip: %q %v", plain, err)
	}
	before, _ := os.ReadFile(p)
	if made, err := EnsureKey(p); made || err != nil {
		t.Fatalf("second call: made=%v err=%v", made, err)
	}
	if after, _ := os.ReadFile(p); string(after) != string(before) {
		t.Fatal("an existing key was replaced")
	}
	if st, _ := os.Stat(p); st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", st.Mode().Perm())
	}
}

func TestParseIdentitiesAndIsWrongKeyAndReseal(t *testing.T) {
	t.Parallel()
	oldID := newID(t)
	newID1 := newID(t)
	oldKeyText := "# comment\n" + oldID.String() + "\n"
	otherID := newID(t)

	ids, err := ParseIdentities(oldKeyText + "\n" + otherID.String() + "\n")
	if err != nil || len(ids) != 2 {
		t.Fatalf("ParseIdentities = %d, %v", len(ids), err)
	}
	for _, empty := range []string{"", "# only comments\n  \n"} {
		if _, err := ParseIdentities(empty); err == nil {
			t.Errorf("ParseIdentities(%q) succeeded", empty)
		}
	}
	badSecret := "not-an-age-secret-SUPERSECRET-marker"
	_, err = ParseIdentities(badSecret)
	if err == nil {
		t.Fatal("malformed age key succeeded")
	}
	if strings.Contains(err.Error(), "SUPERSECRET") {
		t.Errorf("ParseIdentities leaked input in error: %v", err)
	}

	oldFile := keyFile(t, oldID)
	newFile := keyFile(t, newID1)
	sealed, err := Seal(oldFile, []byte("secret-payload"))
	if err != nil {
		t.Fatal(err)
	}

	_, err = Open(newFile, sealed)
	if !IsWrongKey(err) {
		t.Fatalf("expected IsWrongKey on mismatched key, got %v", err)
	}
	_, err = Open(newFile, []byte("not-age-ciphertext"))
	if IsWrongKey(err) {
		t.Errorf("corrupt ciphertext flagged as wrong key: %v", err)
	}
	_, err = Open(filepath.Join(t.TempDir(), "missing"), sealed)
	if IsWrongKey(err) {
		t.Errorf("missing key file flagged as wrong key: %v", err)
	}

	// Wrong old key fails.
	if _, _, err := Reseal([]age.Identity{otherID}, newFile, sealed); !IsWrongKey(err) {
		t.Errorf("Reseal with wrong old key = %v", err)
	}
	// Corrupt ciphertext fails without matching wrong key.
	if _, _, err := Reseal(ids, newFile, []byte("corrupt")); err == nil || IsWrongKey(err) {
		t.Errorf("Reseal on corrupt ciphertext = %v", err)
	}

	// Valid old key re-seals to new host key.
	resealed, changed, err := Reseal(ids, newFile, sealed)
	if err != nil || !changed {
		t.Fatalf("Reseal = changed:%v err:%v", changed, err)
	}
	plain, err := Open(newFile, resealed)
	if err != nil || string(plain) != "secret-payload" {
		t.Fatalf("Open(newFile, resealed) = %q, %v", plain, err)
	}
	if _, err := Open(oldFile, resealed); !IsWrongKey(err) {
		t.Errorf("resealed still opens with old key: %v", err)
	}

	// Second Reseal is a no-op when already readable by new host key.
	again, changed, err := Reseal([]age.Identity{otherID}, newFile, resealed)
	if err != nil || changed || !bytes.Equal(again, resealed) {
		t.Errorf("second Reseal = changed:%v err:%v", changed, err)
	}
}
