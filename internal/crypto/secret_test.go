package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestRoundTripAndKeyFile(t *testing.T) {
	dir := t.TempDir()
	SetKeyDir(dir)
	enc, err := EncryptPassword("geheim€ä")
	if err != nil {
		t.Fatal(err)
	}
	if !IsEncrypted(enc) {
		t.Fatal("kein Präfix")
	}
	dec, err := DecryptPassword(enc)
	if err != nil || dec != "geheim€ä" {
		t.Fatalf("got %q %v", dec, err)
	}
	st, err := os.Stat(filepath.Join(dir, keyFileName))
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm()&0o077 != 0 && runtime.GOOS != "windows" {
		t.Fatalf("Schlüsseldatei zu offen: %v", st.Mode())
	}
	// Neuer Prozess (Cache leer) liest denselben Schlüssel.
	SetKeyDir(dir)
	if d2, _ := DecryptPassword(enc); d2 != "geheim€ä" {
		t.Fatal("Schlüssel nicht stabil")
	}
}

func TestLegacyValueStillReadable(t *testing.T) {
	SetKeyDir(t.TempDir())
	block, _ := aes.NewCipher(legacyMachineKey())
	gcm, _ := cipher.NewGCM(block)
	nonce := make([]byte, gcm.NonceSize())
	legacy := encPrefixLegacy + base64.StdEncoding.EncodeToString(gcm.Seal(nonce, nonce, []byte("alt"), nil))
	if d, err := DecryptPassword(legacy); err != nil || d != "alt" {
		t.Fatalf("legacy: %q %v", d, err)
	}
}

func TestFailureNeverReturnsCiphertext(t *testing.T) {
	SetKeyDir(t.TempDir())
	d, err := DecryptPassword("ENC2:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
	if err == nil || d != "" {
		t.Fatalf("erwartet Fehler und leeren String, got %q", d)
	}
}

func TestWriteFailureCleansUpKeyFile(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("POSIX directory permission enforcement not applicable here")
	}
	dir := t.TempDir()
	SetKeyDir(dir)
	roDir := filepath.Join(dir, "readonly_dir")
	if err := os.Mkdir(roDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(roDir, 0o700) })

	SetKeyDir(roDir)
	if _, err := EncryptPassword("secret"); err == nil {
		t.Fatal("expected error when writing key into read-only directory")
	}
	if _, err := os.Stat(filepath.Join(roDir, keyFileName)); !os.IsNotExist(err) {
		t.Fatalf("expected no leftover %s, got err=%v", keyFileName, err)
	}
}
