package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
)

type Cipher struct {
	aead  cipher.AEAD
	keyID string
}

func New(key []byte) (*Cipher, error) {
	if len(key) != 32 {
		return nil, errors.New("master key must be 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCMWithRandomNonce(block)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(key)
	return &Cipher{aead, hex.EncodeToString(sum[:16])}, nil
}
func aad(space, id, key string) []byte {
	b, _ := json.Marshal([]string{"mass.credential.v1", space, id, key})
	return b
}
func (c *Cipher) Seal(space, id string, plaintext []byte) (string, []byte, error) {
	return c.keyID, c.aead.Seal(nil, nil, plaintext, aad(space, id, c.keyID)), nil
}
func (c *Cipher) Open(space, id, key string, ciphertext []byte) ([]byte, error) {
	if key != c.keyID {
		return nil, errors.New("credential key unavailable")
	}
	return c.aead.Open(nil, nil, ciphertext, aad(space, id, key))
}
