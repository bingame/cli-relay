package secrets

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestEncryptionAuthenticatesContextAndTampering(t *testing.T) {
	v, e := New(bytes.Repeat([]byte{1}, 32))
	if e != nil {
		t.Fatal(e)
	}
	plain := []byte("test-secret-value")
	sealed := v.Seal(plain, "provider:api_key")
	if bytes.Contains(sealed, plain) {
		t.Fatal("密文含明文")
	}
	got, e := v.Open(sealed, "provider:api_key")
	if e != nil || !bytes.Equal(got, plain) {
		t.Fatal("解密失败", e)
	}
	if _, e = v.Open(sealed, "another:api_key"); e == nil {
		t.Fatal("接受了不同上下文")
	}
	sealed[len(sealed)-1] ^= 1
	if _, e = v.Open(sealed, "provider:api_key"); e == nil {
		t.Fatal("接受了篡改密文")
	}
}
func TestPassphraseVaultReopenAndWrongPassword(t *testing.T) {
	root := t.TempDir()
	v, e := Open(root, Options{Passphrase: "dummy-passphrase-12345"})
	if e != nil {
		t.Fatal(e)
	}
	b := v.Seal([]byte("confidential"), "test")
	again, e := Open(root, Options{Passphrase: "dummy-passphrase-12345"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = again.Open(b, "test"); e != nil {
		t.Fatal(e)
	}
	if _, e = Open(root, Options{Passphrase: "wrong-passphrase-12345"}); e == nil {
		t.Fatal("错误口令被接受")
	}
	metadata, _ := os.ReadFile(filepath.Join(root, "vault.json"))
	if bytes.Contains(metadata, []byte("dummy-passphrase")) {
		t.Fatal("口令落盘")
	}
}
