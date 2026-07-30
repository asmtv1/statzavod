package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
)

func NewFromBase64(encoded string) (*Envelope, error) {
	key, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		key, err = base64.RawURLEncoding.DecodeString(encoded)
	}
	if err != nil {
		return nil, fmt.Errorf("decode token encryption key: %w", err)
	}
	return New(key)
}

type Envelope struct{ aead cipher.AEAD }

func New(key []byte) (*Envelope, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("token encryption key must be 32 bytes")
	}
	b, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	a, err := cipher.NewGCM(b)
	if err != nil {
		return nil, err
	}
	return &Envelope{aead: a}, nil
}
func (e *Envelope) Encrypt(plain []byte) (ciphertext, nonce []byte, err error) {
	nonce = make([]byte, e.aead.NonceSize())
	if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, nil, err
	}
	return e.aead.Seal(nil, nonce, plain, nil), nonce, nil
}
func (e *Envelope) Decrypt(ciphertext, nonce []byte) ([]byte, error) {
	return e.aead.Open(nil, nonce, ciphertext, nil)
}
