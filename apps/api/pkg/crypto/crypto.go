package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
)

// ErrInvalidKey is returned when the encryption key is invalid
var ErrInvalidKey = errors.New("invalid encryption key")

// ErrDecryptionFailed is returned when decryption fails
var ErrDecryptionFailed = errors.New("decryption failed")

// deriveKey creates a 32-byte key from the input key using SHA-256
func deriveKey(key string) []byte {
	hash := sha256.Sum256([]byte(key))
	return hash[:]
}

// Encrypt encrypts plaintext using AES-GCM with the given key
func Encrypt(plaintext, key string) (string, error) {
	if key == "" {
		return "", ErrInvalidKey
	}

	derivedKey := deriveKey(key)

	block, err := aes.NewCipher(derivedKey)
	if err != nil {
		return "", err
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}

	ciphertext := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

// Decrypt decrypts ciphertext using AES-GCM with the given key
func Decrypt(ciphertext, key string) (string, error) {
	if key == "" {
		return "", ErrInvalidKey
	}

	if ciphertext == "" {
		return "", ErrDecryptionFailed
	}

	derivedKey := deriveKey(key)

	data, err := base64.StdEncoding.DecodeString(ciphertext)
	if err != nil {
		return "", ErrDecryptionFailed
	}

	block, err := aes.NewCipher(derivedKey)
	if err != nil {
		return "", err
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}

	nonceSize := gcm.NonceSize()
	if len(data) < nonceSize {
		return "", ErrDecryptionFailed
	}

	nonce, encryptedData := data[:nonceSize], data[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, encryptedData, nil)
	if err != nil {
		return "", ErrDecryptionFailed
	}

	return string(plaintext), nil
}
