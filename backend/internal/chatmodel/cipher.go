// 本文件负责模型 API Key 的落库加密。
//
// 为什么必须加密：.env 里的密钥只躺在本地文件系统，而库里的密钥会跟着
// pg_dump、备份、导出一起走。把一份 dump 发给别人排查问题，明文 Key 就跟着出去了。
//
// 为什么用 AES-GCM 而不是自己拼 HMAC + CBC：GCM 自带完整性校验，
// 密文被改一个字节解密就会失败，不会"解出一段乱码当 Key 用"然后
// 让厂商以 401 拒绝——那种错误最难查，因为配置看起来是对的。
package chatmodel

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	// masterKeySize 是 AES-256 的密钥长度。AES 只接受 16/24/32，
	// 这里固定 32，等于禁用 AES-128/192 两种弱一些的形态。
	masterKeySize = 32

	// masterKeyFile 是自动生成的主密钥文件名。
	masterKeyFile = "master.key"
)

// ErrMasterKeyInvalid 表示主密钥不是 base64 的 32 字节。
//
// 这类错误在启动时就该失败：带着坏密钥跑起来，用户第一次保存模型配置
// 才会看到错误，那时已经以为自己配好了。
var ErrMasterKeyInvalid = errors.New("master key must be 32 bytes, base64 encoded")

// ErrDecryptFailed 表示密文解不开（主密钥换了、密文被改过、数据损坏）。
//
// 必须单独成型而不是返回空串：空串会让上层构造出一个"没有 Key 的请求"，
// 于是厂商回 401，人去查 Key 有没有过期——而真正的原因是本地主密钥不匹配。
var ErrDecryptFailed = errors.New("cannot decrypt stored credential")

// Cipher 是模型 API Key 的加解密器。
type Cipher struct {
	aead cipher.AEAD
}

// NewCipher 用 32 字节主密钥构造加解密器。
func NewCipher(key []byte) (*Cipher, error) {
	if len(key) != masterKeySize {
		return nil, ErrMasterKeyInvalid
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("new aes cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("new gcm: %w", err)
	}
	return &Cipher{aead: aead}, nil
}

// AADForOwner 返回绑定"这条配置属于谁"的附加认证数据。
//
// GCM 的 AAD 参与完整性校验但不进密文：同一段密文被复制到另一个人的行上时，
// 解密会用不同的 AAD，Open 直接失败。效果是"搬行"不会让 B 悄悄用上 A 的 Key
// 去花 A 的钱，而是得到"解不开，请重填"——后者能查，前者查不出来。
func AADForOwner(ownerID int64) []byte {
	return []byte("chat_model:" + strconv.FormatInt(ownerID, 10))
}

// Encrypt 返回 nonce||ciphertext。
//
// nonce 每条随机生成并拼在密文前面：GCM 复用 nonce 是最致命的误用
// （能恢复出明文之间的关系），所以这里每次调用都重新读随机数，
// 不接受调用方传入，避免"为了省事把 nonce 存成常量"。
//
// aad 必须与解密时一致；本模块固定用 AADForOwner(ownerID)。
func (c *Cipher) Encrypt(plain string, aad []byte) ([]byte, error) {
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("read nonce: %w", err)
	}
	// Seal 的第一个参数是 dst：传 nonce 本身，密文就紧跟在它后面，
	// 于是返回值天然是 nonce||ciphertext，不需要再拼一次。
	return c.aead.Seal(nonce, nonce, []byte(plain), aad), nil
}

// Decrypt 解开 Encrypt 的产物。
func (c *Cipher) Decrypt(blob []byte, aad []byte) (string, error) {
	size := c.aead.NonceSize()
	if len(blob) <= size {
		return "", ErrDecryptFailed
	}
	plain, err := c.aead.Open(nil, blob[:size], blob[size:], aad)
	if err != nil {
		// 不把 err 原文往上带：它会包含 GCM 的底层描述，
		// 对用户没有意义，而真正要说的只有"解不开，请重填 Key"。
		return "", ErrDecryptFailed
	}
	return string(plain), nil
}

// LoadMasterKey 取 32 字节主密钥。
//
// 优先级：MINDOS_MASTER_KEY（base64）→ dir/master.key → 随机生成后写入文件。
//
// 为什么允许自动生成而不是强制用户先准备一个：那是给自托管团队的门槛，
// 不是给本地单用户的。自动生成的代价写在 docs/design/rag.md 的已知限制里
// ——删了这个文件就再也解不开已存的 Key，只能重填。
func LoadMasterKey(encoded, dir string) ([]byte, error) {
	if trimmed := strings.TrimSpace(encoded); trimmed != "" {
		key, err := base64.StdEncoding.DecodeString(trimmed)
		if err != nil {
			return nil, fmt.Errorf("MINDOS_MASTER_KEY is not valid base64: %w", err)
		}
		if len(key) != masterKeySize {
			return nil, fmt.Errorf("MINDOS_MASTER_KEY: %w", ErrMasterKeyInvalid)
		}
		return key, nil
	}

	path := filepath.Join(dir, masterKeyFile)
	raw, err := os.ReadFile(path)
	switch {
	case err == nil:
		key, decodeErr := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
		if decodeErr != nil {
			return nil, fmt.Errorf("read %s: %w", path, decodeErr)
		}
		if len(key) != masterKeySize {
			return nil, fmt.Errorf("%s: %w", path, ErrMasterKeyInvalid)
		}
		return key, nil
	case !errors.Is(err, fs.ErrNotExist):
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	key := make([]byte, masterKeySize)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, fmt.Errorf("generate master key: %w", err)
	}
	// 0700：这个目录里只有主密钥，别的进程不该能列目录。
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create key dir %s: %w", dir, err)
	}
	// 0600：只有属主能读写。权限只在创建时生效，所以走的必然是"文件不存在"这条路径。
	if err := os.WriteFile(path, []byte(base64.StdEncoding.EncodeToString(key)+"\n"), 0o600); err != nil {
		return nil, fmt.Errorf("write %s: %w", path, err)
	}
	return key, nil
}
