package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"usenet-goup/internal/sysproc"
)

// encPrefix2 markiert Werte, die mit dem zufälligen Schlüssel aus keyFile verschlüsselt sind.
// encPrefixLegacy markiert alte Werte (Schlüssel aus Hostname/Machine-ID, nur noch zum Lesen).
const (
	encPrefix2      = "ENC2:"
	encPrefixLegacy = "ENC:"
	keyFileName     = "secret.key"
)

var (
	keyDir     string
	keyMu      sync.Mutex
	cachedKey  []byte
	legacyOnce sync.Once
	legacyKey  []byte
)

// SetKeyDir legt fest, wo secret.key liegt (gleiches Verzeichnis wie config.json).
func SetKeyDir(dir string) {
	keyMu.Lock()
	defer keyMu.Unlock()
	keyDir = dir
	cachedKey = nil
}

// IsEncrypted meldet, ob ein Wert bereits verschlüsselt ist.
func IsEncrypted(s string) bool {
	return strings.HasPrefix(s, encPrefix2) || strings.HasPrefix(s, encPrefixLegacy)
}

func fileKey() ([]byte, error) {
	keyMu.Lock()
	defer keyMu.Unlock()
	if cachedKey != nil {
		return cachedKey, nil
	}
	dir := keyDir
	if dir == "" {
		return nil, errors.New("Schlüsselverzeichnis nicht gesetzt")
	}
	path := filepath.Join(dir, keyFileName)
	if b, err := os.ReadFile(path); err == nil && len(b) == 32 {
		_ = os.Chmod(path, 0o600)
		cachedKey = b
		return b, nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	key := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, err
	}
	// O_EXCL: zwei gleichzeitige Starts dürfen sich den Schlüssel nicht gegenseitig überschreiben.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if b, rErr := os.ReadFile(path); rErr == nil && len(b) == 32 {
			_ = os.Chmod(path, 0o600)
			cachedKey = b
			return b, nil
		}
		return nil, err
	}
	if _, err := f.Write(key); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return nil, err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return nil, err
	}
	_ = os.Chmod(path, 0o600)
	cachedKey = key
	return key, nil
}

// legacyMachineKey rekonstruiert den Schlüssel der Versionen <= 1.28 (nur zum Entschlüsseln alter Werte).
func legacyMachineKey() []byte {
	legacyOnce.Do(func() {
		var parts []string
		if h, err := os.Hostname(); err == nil {
			parts = append(parts, h)
		}
		parts = append(parts, runtime.GOOS, runtime.GOARCH)
		if runtime.GOOS == "windows" {
			regCmd := exec.Command("reg", "query", `HKLM\SOFTWARE\Microsoft\Cryptography`, "/v", "MachineGuid")
			sysproc.HideWindow(regCmd)
			if out, err := regCmd.Output(); err == nil {
				parts = append(parts, strings.TrimSpace(string(out)))
			}
		} else if b, err := os.ReadFile("/etc/machine-id"); err == nil {
			parts = append(parts, strings.TrimSpace(string(b)))
		}
		sum := sha256.Sum256([]byte("usenet-goup-v1|" + strings.Join(parts, "|")))
		legacyKey = sum[:]
	})
	return legacyKey
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// EncryptPassword verschlüsselt mit AES-256-GCM und dem zufälligen Schlüssel aus secret.key.
func EncryptPassword(plain string) (string, error) {
	if plain == "" || IsEncrypted(plain) {
		return plain, nil
	}
	key, err := fileKey()
	if err != nil {
		return "", err
	}
	gcm, err := newGCM(key)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	ciphertext := gcm.Seal(nonce, nonce, []byte(plain), nil)
	return encPrefix2 + base64.StdEncoding.EncodeToString(ciphertext), nil
}

// DecryptPassword liefert bei einem Fehler "" und den Fehler zurück, niemals den verschlüsselten String.
func DecryptPassword(stored string) (string, error) {
	var key []byte
	var payload string
	switch {
	case stored == "":
		return "", nil
	case strings.HasPrefix(stored, encPrefix2):
		k, err := fileKey()
		if err != nil {
			return "", err
		}
		key, payload = k, strings.TrimPrefix(stored, encPrefix2)
	case strings.HasPrefix(stored, encPrefixLegacy):
		key, payload = legacyMachineKey(), strings.TrimPrefix(stored, encPrefixLegacy)
	default:
		return stored, nil
	}
	raw, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return "", err
	}
	gcm, err := newGCM(key)
	if err != nil {
		return "", err
	}
	if len(raw) < gcm.NonceSize() {
		return "", fmt.Errorf("ungültiger Geheimtext")
	}
	nonce, ciphertext := raw[:gcm.NonceSize()], raw[gcm.NonceSize():]
	plain, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("Passwort konnte nicht entschlüsselt werden (Schlüssel geändert?): %w", err)
	}
	return string(plain), nil
}
