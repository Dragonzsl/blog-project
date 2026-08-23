package identity

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

const (
	passwordMemory      = 64 * 1024
	passwordIterations  = 3
	passwordParallelism = 1
	passwordSaltLength  = 16
	passwordKeyLength   = 32
	recoveryCodeCount   = 10
)

type passwordHasher struct{}

func (passwordHasher) Hash(password string) (string, error) {
	if err := validatePassword(password); err != nil {
		return "", err
	}
	salt := make([]byte, passwordSaltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate password salt: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, passwordIterations, passwordMemory, passwordParallelism, passwordKeyLength)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version,
		passwordMemory,
		passwordIterations,
		passwordParallelism,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	), nil
}

func (passwordHasher) Verify(encoded, password string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, errors.New("invalid password hash format")
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false, errors.New("unsupported password hash version")
	}
	var memory, iterations uint32
	var parallelism uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &iterations, &parallelism); err != nil {
		return false, errors.New("invalid password hash parameters")
	}
	if memory < 19*1024 || memory > 256*1024 || iterations < 1 || iterations > 10 || parallelism < 1 || parallelism > 4 {
		return false, errors.New("password hash parameters outside safety bounds")
	}
	salt, err := base64.RawStdEncoding.Strict().DecodeString(parts[4])
	if err != nil || len(salt) < 16 || len(salt) > 64 {
		return false, errors.New("invalid password hash salt")
	}
	want, err := base64.RawStdEncoding.Strict().DecodeString(parts[5])
	if err != nil || len(want) < 16 || len(want) > 64 {
		return false, errors.New("invalid password hash key")
	}
	got := argon2.IDKey([]byte(password), salt, iterations, memory, parallelism, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

func validatePassword(password string) error {
	if utf8.RuneCountInString(password) < 12 {
		return errors.New("密码至少需要 12 个字符")
	}
	if len(password) > 128 || !utf8.ValidString(password) {
		return errors.New("密码不能超过 128 字节")
	}
	return nil
}

type protector struct {
	aead cipher.AEAD
	key  []byte
}

func newProtector(secret []byte) (*protector, error) {
	if len(secret) < 32 {
		return nil, errors.New("auth secret must be at least 32 bytes")
	}
	derived := sha256.Sum256(secret)
	block, err := aes.NewCipher(derived[:])
	if err != nil {
		return nil, fmt.Errorf("create auth cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create auth AEAD: %w", err)
	}
	return &protector{aead: aead, key: derived[:]}, nil
}

func (p *protector) Encrypt(plaintext string) ([]byte, error) {
	nonce := make([]byte, p.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("generate encryption nonce: %w", err)
	}
	return p.aead.Seal(nonce, nonce, []byte(plaintext), []byte("blog:totp:v1")), nil
}

func (p *protector) Decrypt(ciphertext []byte) (string, error) {
	if len(ciphertext) < p.aead.NonceSize() {
		return "", errors.New("encrypted value is truncated")
	}
	nonce := ciphertext[:p.aead.NonceSize()]
	plaintext, err := p.aead.Open(nil, nonce, ciphertext[p.aead.NonceSize():], []byte("blog:totp:v1"))
	if err != nil {
		return "", errors.New("decrypt protected value")
	}
	return string(plaintext), nil
}

func (p *protector) MAC(label, value string) []byte {
	mac := hmac.New(sha256.New, p.key)
	mac.Write([]byte(label))
	mac.Write([]byte{0})
	mac.Write([]byte(value))
	return mac.Sum(nil)
}

func randomToken(bytes int) (string, error) {
	value := make([]byte, bytes)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("generate random token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func tokenHash(token string) []byte {
	hash := sha256.Sum256([]byte(token))
	return hash[:]
}

func generateRecoveryCodes(p *protector) ([]string, [][]byte, error) {
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	codes := make([]string, 0, recoveryCodeCount)
	hashes := make([][]byte, 0, recoveryCodeCount)
	for range recoveryCodeCount {
		random := make([]byte, 16)
		if _, err := rand.Read(random); err != nil {
			return nil, nil, fmt.Errorf("generate recovery code: %w", err)
		}
		characters := make([]byte, 16)
		for index := range characters {
			characters[index] = alphabet[int(random[index])%len(alphabet)]
		}
		code := strings.Join([]string{
			string(characters[0:4]),
			string(characters[4:8]),
			string(characters[8:12]),
			string(characters[12:16]),
		}, "-")
		codes = append(codes, code)
		hashes = append(hashes, p.MAC("recovery-code", normalizeRecoveryCode(code)))
	}
	return codes, hashes, nil
}

func normalizeRecoveryCode(code string) string {
	normalized := strings.ReplaceAll(strings.TrimSpace(code), "-", "")
	normalized = strings.ReplaceAll(normalized, " ", "")
	return strings.ToUpper(normalized)
}

func normalizeTOTP(code string) string {
	return strings.ReplaceAll(strings.TrimSpace(code), " ", "")
}

func constantStringEqual(left, right string) bool {
	return subtle.ConstantTimeCompare([]byte(left), []byte(right)) == 1
}
