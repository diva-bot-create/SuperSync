package sqlcipher

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// The fixtures are pyrekordbox's test databases (MIT licensed): a real
// rekordbox 6 master.db and the same database decrypted by SQLCipher.
// Set REKORDBOX_TESTDATA to the folder holding master_locked.db and
// master_unlocked.db to run these.
const passphrase = "402fd482c38817c35ffa8ffb8c7d93143b749e7d315df7a81732a1ff43608497"

func fixtures(t *testing.T) (locked, unlocked string) {
	dir := os.Getenv("REKORDBOX_TESTDATA")
	if dir == "" {
		t.Skip("REKORDBOX_TESTDATA not set")
	}
	return filepath.Join(dir, "master_locked.db"), filepath.Join(dir, "master_unlocked.db")
}

func TestRoundTrip(t *testing.T) {
	locked, _ := fixtures(t)
	plain, salt, err := Decrypt(locked, passphrase)
	if err != nil {
		t.Fatal(err)
	}
	enc, err := Encrypt(plain, passphrase, salt)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "master.db")
	os.WriteFile(p, enc, 0o644)
	again, _, err := Decrypt(p, passphrase)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(again, plain) {
		t.Fatal("round trip changed the data")
	}
	if _, _, err := Decrypt(p, "wrong"); err != ErrWrongKey {
		t.Fatalf("wrong key: %v", err)
	}
}

// DumpPlain writes the decrypted image for external checks (set DUMP_PLAIN).
func TestDumpPlain(t *testing.T) {
	out := os.Getenv("DUMP_PLAIN")
	if out == "" {
		t.Skip()
	}
	locked, _ := fixtures(t)
	plain, _, err := Decrypt(locked, passphrase)
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(out, plain, 0o644)
}
