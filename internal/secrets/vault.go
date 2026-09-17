package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bingame/cli-relay/internal/safeio"
	"github.com/zalando/go-keyring"
	"golang.org/x/crypto/argon2"
)

type Vault struct{ aead cipher.AEAD }
type metadata struct {
	Version int    `json:"version"`
	Backend string `json:"backend"`
	Salt    string `json:"salt,omitempty"`
	Check   []byte `json:"check"`
}
type Options struct {
	Passphrase string
	Prompt     func(confirm bool) (string, error)
}

func New(key []byte) (*Vault, error) {
	b, e := aes.NewCipher(key)
	if e != nil {
		return nil, e
	}
	a, e := cipher.NewGCM(b)
	if e != nil {
		return nil, e
	}
	return &Vault{aead: a}, nil
}
func (v *Vault) Seal(plain []byte, context string) []byte {
	nonce := make([]byte, v.aead.NonceSize())
	if _, e := rand.Read(nonce); e != nil {
		panic(e)
	}
	return v.aead.Seal(nonce, nonce, plain, []byte(context))
}
func (v *Vault) Open(data []byte, context string) ([]byte, error) {
	n := v.aead.NonceSize()
	if len(data) < n {
		return nil, fmt.Errorf("加密数据损坏")
	}
	p, e := v.aead.Open(nil, data[:n], data[n:], []byte(context))
	if e != nil {
		return nil, fmt.Errorf("无法解密：密钥不匹配或数据损坏")
	}
	return p, nil
}

func Open(root string, opts Options) (*Vault, error) {
	if e := safeio.EnsurePrivateDir(root); e != nil {
		return nil, e
	}
	path := filepath.Join(root, "vault.json")
	abs, e := filepath.Abs(root)
	if e != nil {
		return nil, e
	}
	sum := sha256.Sum256([]byte(abs))
	account := hex.EncodeToString(sum[:16])
	var m metadata
	var key []byte
	raw, e := safeio.ReadRegular(path, 8192)
	fresh := errors.Is(e, os.ErrNotExist)
	if e != nil && !fresh {
		return nil, e
	}
	if !fresh {
		if json.Unmarshal(raw, &m) != nil || m.Version != 1 {
			return nil, fmt.Errorf("无法识别密钥库元数据")
		}
	}
	if fresh {
		m.Version = 1
		if opts.Passphrase == "" {
			key = make([]byte, 32)
			if _, e = rand.Read(key); e != nil {
				return nil, e
			}
			if e = keyring.Set("relay", account, base64.StdEncoding.EncodeToString(key)); e == nil {
				m.Backend = "keyring"
			}
		}
		if m.Backend == "" {
			m.Backend = "passphrase"
			salt := make([]byte, 32)
			if _, e = rand.Read(salt); e != nil {
				return nil, e
			}
			m.Salt = base64.StdEncoding.EncodeToString(salt)
		}
	}
	switch m.Backend {
	case "keyring":
		if !fresh {
			encoded, err := keyring.Get("relay", account)
			if err != nil {
				return nil, fmt.Errorf("操作系统密钥库不可用，不能解密已有数据")
			}
			key, e = base64.StdEncoding.DecodeString(encoded)
			if e != nil {
				return nil, fmt.Errorf("密钥库条目损坏")
			}
		}
	case "passphrase":
		pass := opts.Passphrase
		if pass == "" && opts.Prompt != nil {
			pass, e = opts.Prompt(fresh)
			if e != nil {
				return nil, e
			}
		}
		if len(pass) < 12 {
			return nil, fmt.Errorf("本地加密需要至少 12 字符口令；设置 RELAY_PASSPHRASE 或在终端输入")
		}
		salt, e := base64.StdEncoding.DecodeString(m.Salt)
		if e != nil || len(salt) != 32 {
			return nil, fmt.Errorf("密钥库 salt 损坏")
		}
		machine, e := machineID()
		if e != nil {
			return nil, e
		}
		key = argon2.IDKey([]byte(pass), append(salt, []byte(machine)...), 3, 64*1024, 2, 32)
	default:
		return nil, fmt.Errorf("未知密钥库后端")
	}
	v, e := New(key)
	for i := range key {
		key[i] = 0
	}
	if e != nil {
		return nil, e
	}
	if fresh {
		m.Check = v.Seal([]byte("relay-vault-v1"), "vault-check")
		data, _ := json.MarshalIndent(m, "", "  ")
		if e = safeio.WriteFile(path, data, 0600); e != nil {
			return nil, e
		}
	} else {
		if _, e = v.Open(m.Check, "vault-check"); e != nil {
			return nil, e
		}
	}
	return v, nil
}
func machineID() (string, error) {
	if id, err := nativeMachineID(); err == nil && id != "" {
		return id, nil
	}
	for _, p := range []string{"/etc/machine-id", "/var/lib/dbus/machine-id"} {
		if b, e := os.ReadFile(p); e == nil && len(b) > 0 {
			return strings.TrimSpace(string(b)), nil
		}
	}
	host, e := os.Hostname()
	if e != nil {
		return "", e
	}
	u, e := user.Current()
	if e != nil {
		return "", e
	}
	return host + ":" + u.Uid, nil
}

// Redact 是输出边界的防漏措施，不会改变加密存储的原始数据。
func Redact(text string, values []string) string {
	values = append([]string{}, values...)
	sort.SliceStable(values, func(i, j int) bool { return len(values[i]) > len(values[j]) })
	for _, v := range values {
		if len(v) > 0 {
			text = strings.ReplaceAll(text, v, "[已隐藏凭据]")
		}
	}
	return text
}
