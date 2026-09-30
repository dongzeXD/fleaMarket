// Package crypto 提供对称加密与摘要工具。
//
// 存在的理由：手机号这类个人敏感信息需要「可解密展示」+「可等值检索」两种能力，
// 而单一手段无法同时满足——随机化加密不可检索，确定性摘要不可还原。
// 本包把这对组合封装成一个类型，避免各服务各写一套且写错。
//
// 泛用性约束（doc.md §5.1）：本包不知道加密的是手机号还是别的什么，
// 只提供「敏感字段的双轨存储」这一通用机制。
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
)

// ErrInvalidCiphertext 密文格式非法或校验失败。
var ErrInvalidCiphertext = errors.New("crypto: invalid ciphertext")

// SensitiveField 敏感字段的加密与摘要器。
//
// 两条轨道：
//   - Encrypt/Decrypt 走 AES-GCM，每次使用随机 nonce，
//     因此同一明文两次加密结果不同——这是安全属性（防止从密文相等推断明文相等），
//     但也意味着密文不能用于唯一索引或等值查询。
//   - Hash 走 HMAC-SHA256，确定性输出，承担唯一约束与检索。
//     用 HMAC 而非裸 SHA256：手机号空间很小（约 10^10），
//     裸哈希可被彩虹表穷举反推，加密钥后则不行。
//
// 两条轨道使用独立密钥，避免一处泄漏同时丧失两种保护。
type SensitiveField struct {
	aead    cipher.AEAD
	hmacKey []byte
}

// NewSensitiveField 创建加密器。
//
// encKey 必须是 16、24 或 32 字节（对应 AES-128/192/256）。
// hmacKey 建议 32 字节，长度不限但过短会降低摘要强度。
// 两个密钥必须不同，且由环境变量或密钥管理服务注入，不得硬编码。
func NewSensitiveField(encKey, hmacKey []byte) (*SensitiveField, error) {
	switch len(encKey) {
	case 16, 24, 32:
	default:
		return nil, fmt.Errorf("crypto: encKey must be 16/24/32 bytes, got %d", len(encKey))
	}
	if len(hmacKey) < 16 {
		return nil, fmt.Errorf("crypto: hmacKey must be at least 16 bytes, got %d", len(hmacKey))
	}
	if subtle.ConstantTimeCompare(encKey, hmacKey) == 1 {
		return nil, errors.New("crypto: encKey and hmacKey must differ")
	}

	block, err := aes.NewCipher(encKey)
	if err != nil {
		return nil, fmt.Errorf("crypto: new cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("crypto: new gcm: %w", err)
	}

	key := make([]byte, len(hmacKey))
	copy(key, hmacKey)
	return &SensitiveField{aead: aead, hmacKey: key}, nil
}

// Encrypt 加密明文，返回 base64 编码的「nonce || 密文 || tag」。
//
// nonce 前置存储而非单独存一列：它不是秘密，且与密文一一对应，
// 分开存只会增加出错机会（比如忘了一起迁移）。
func (s *SensitiveField) Encrypt(plain string) (string, error) {
	if plain == "" {
		return "", nil
	}
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("crypto: generate nonce: %w", err)
	}
	sealed := s.aead.Seal(nonce, nonce, []byte(plain), nil)
	return base64.StdEncoding.EncodeToString(sealed), nil
}

// Decrypt 解密。密文被篡改时返回 ErrInvalidCiphertext（GCM 自带完整性校验）。
func (s *SensitiveField) Decrypt(encoded string) (string, error) {
	if encoded == "" {
		return "", nil
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", ErrInvalidCiphertext
	}
	ns := s.aead.NonceSize()
	if len(raw) < ns {
		return "", ErrInvalidCiphertext
	}
	plain, err := s.aead.Open(nil, raw[:ns], raw[ns:], nil)
	if err != nil {
		return "", ErrInvalidCiphertext
	}
	return string(plain), nil
}

// Hash 计算确定性摘要，输出 64 位小写十六进制字符串（适合 char(64) 列）。
func (s *SensitiveField) Hash(plain string) string {
	if plain == "" {
		return ""
	}
	mac := hmac.New(sha256.New, s.hmacKey)
	mac.Write([]byte(plain))
	return hex.EncodeToString(mac.Sum(nil))
}

// MaskPhone 脱敏手机号，如 "13800138000" → "138****8000"。
//
// 放在本包而非业务代码：脱敏规则若散落各处，迟早有一处写错而泄漏完整号码。
// 非 11 位输入按通用规则处理（保留首尾各 3 位），不报错——
// 脱敏是展示层需求，不应因格式意外而中断整个响应。
func MaskPhone(phone string) string {
	r := []rune(phone)
	switch {
	case len(r) == 0:
		return ""
	case len(r) == 11:
		return string(r[:3]) + "****" + string(r[7:])
	case len(r) <= 6:
		// 太短无法既脱敏又保留信息，全部遮蔽。
		return "****"
	default:
		return string(r[:3]) + "****" + string(r[len(r)-3:])
	}
}

// MaskEmail 脱敏邮箱，如 "zhangsan@stu.pku.edu.cn" → "zh***@stu.pku.edu.cn"。
// 域名保留，因为它是校园认证中需要让用户确认的部分。
func MaskEmail(email string) string {
	at := -1
	for i, c := range email {
		if c == '@' {
			at = i
			break
		}
	}
	if at <= 0 {
		return "****"
	}
	local := []rune(email[:at])
	domain := email[at:]
	if len(local) <= 2 {
		return "***" + domain
	}
	return string(local[:2]) + "***" + domain
}

// MaskTail 保留前 n 位，其余用 * 遮蔽。用于学号等无固定格式的标识。
func MaskTail(s string, keep int) string {
	r := []rune(s)
	if keep < 0 {
		keep = 0
	}
	if len(r) <= keep {
		return string(r)
	}
	out := make([]rune, 0, len(r))
	out = append(out, r[:keep]...)
	for i := keep; i < len(r); i++ {
		out = append(out, '*')
	}
	return string(out)
}
