package secrets

import (
	"bytes"
	"testing"
)

func TestCipherBindingAndNonce(t *testing.T) {
	c, err := New(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	key, a, _ := c.Seal("space", "credential", []byte("secret"))
	_, b, _ := c.Seal("space", "credential", []byte("secret"))
	if bytes.Equal(a, b) || bytes.Contains(a, []byte("secret")) {
		t.Fatal("nonce reuse or plaintext")
	}
	plain, err := c.Open("space", "credential", key, a)
	if err != nil || string(plain) != "secret" {
		t.Fatal(err)
	}
	for _, scope := range [][3]string{{"other", "credential", key}, {"space", "other", key}, {"space", "credential", "other"}} {
		if _, err = c.Open(scope[0], scope[1], scope[2], a); err == nil {
			t.Fatal("accepted mismatched AAD")
		}
	}
	a[len(a)-1] ^= 1
	if _, err = c.Open("space", "credential", key, a); err == nil {
		t.Fatal("accepted tampering")
	}
}
