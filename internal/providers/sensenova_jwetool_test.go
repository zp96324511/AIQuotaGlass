package providers

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"encoding/base64"
	"fmt"
	"math/big"
	"strings"
)

// Test-only helpers: generate an RSA key, encode/decode the JWK exponent, and
// decrypt a JWE token with the private key. They live in a _test.go file so
// nothing ships in production; they exist only to validate sensenovaJWEEncrypt
// against the JWE spec the server enforces.

func rsaGenerateKey() (*rsa.PrivateKey, error) {
	return rsa.GenerateKey(rand.Reader, 2048)
}

// bigIntBytes encodes an RSA exponent (int) as the minimal big-endian byte
// slice the JWK "e" field uses (e.g. 65537 → [0x01, 0x00, 0x01]).
func bigIntBytes(e int) []byte {
	b := new(big.Int).SetInt64(int64(e)).Bytes()
	if len(b) == 0 {
		b = []byte{0}
	}
	return b
}

// sensenovaJWEDecrypt reverses sensenovaJWEEncrypt using the matching private
// key. It parses the compact token, RSA-OAEP-decrypts (SHA-1) the wrapped CEK,
// then AES-256-GCM-opens the ciphertext+tag with the base64url protected
// header as AAD. The token uses the SenseNova 5-part format (tag is a separate
// segment). Test-only — production never holds a private key.
func sensenovaJWEDecrypt(priv *rsa.PrivateKey, token string) ([]byte, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 5 {
		return nil, fmt.Errorf("not a 5-part JWE")
	}
	protected := parts[0]
	encKey, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, err
	}
	iv, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, err
	}
	ct, err := base64.RawURLEncoding.DecodeString(parts[3])
	if err != nil {
		return nil, err
	}
	tag, err := base64.RawURLEncoding.DecodeString(parts[4])
	if err != nil {
		return nil, err
	}
	cek, err := rsa.DecryptOAEP(sha1.New(), nil, priv, encKey, nil)
	if err != nil {
		return nil, fmt.Errorf("rsa-oaep decrypt: %w", err)
	}
	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	// GCM Open expects ciphertext||tag concatenated.
	return aead.Open(nil, iv, append(ct, tag...), []byte(protected))
}
