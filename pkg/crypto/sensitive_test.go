package crypto

import (
	"strings"
	"testing"
)

var (
	encKey  = []byte("0123456789abcdef0123456789abcdef") // 32B
	hmacKey = []byte("fedcba9876543210fedcba9876543210")
)

func newField(t *testing.T) *SensitiveField {
	t.Helper()
	f, err := NewSensitiveField(encKey, hmacKey)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestNewSensitiveFieldValidation(t *testing.T) {
	if _, err := NewSensitiveField([]byte("tooshort"), hmacKey); err == nil {
		t.Fatal("非法长度的 encKey 应报错")
	}
	if _, err := NewSensitiveField(encKey, []byte("short")); err == nil {
		t.Fatal("过短的 hmacKey 应报错")
	}
	// 两把密钥相同会让一处泄漏同时丧失两种保护。
	if _, err := NewSensitiveField(encKey, encKey); err == nil {
		t.Fatal("encKey 与 hmacKey 相同应报错")
	}
	for _, n := range []int{16, 24, 32} {
		if _, err := NewSensitiveField(make([]byte, n), hmacKey); err != nil {
			t.Errorf("%d 字节 encKey 应被接受: %v", n, err)
		}
	}
}

func TestEncryptDecryptRoundTrip(t *testing.T) {
	f := newField(t)
	for _, plain := range []string{"13800138000", "zhangsan@stu.pku.edu.cn", "2021010101", "中文姓名"} {
		ct, err := f.Encrypt(plain)
		if err != nil {
			t.Fatal(err)
		}
		if ct == plain {
			t.Fatalf("密文与明文相同: %q", ct)
		}
		got, err := f.Decrypt(ct)
		if err != nil {
			t.Fatal(err)
		}
		if got != plain {
			t.Fatalf("往返不一致: got %q want %q", got, plain)
		}
	}
}

// 这是本包存在的核心理由：同一明文两次加密结果必须不同（随机 nonce），
// 因此密文不可用于唯一索引——这正是需要 Hash 轨道的原因。
func TestEncryptIsNonDeterministic(t *testing.T) {
	f := newField(t)
	a, err := f.Encrypt("13800138000")
	if err != nil {
		t.Fatal(err)
	}
	b, err := f.Encrypt("13800138000")
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("两次加密结果相同，说明 nonce 未随机化，可从密文相等推断明文相等")
	}
}

// Hash 必须确定性，否则无法承担唯一约束与登录检索。
func TestHashIsDeterministic(t *testing.T) {
	f := newField(t)
	a := f.Hash("13800138000")
	b := f.Hash("13800138000")
	if a != b {
		t.Fatal("同一明文的摘要应相同")
	}
	if len(a) != 64 {
		t.Fatalf("摘要应为 64 位十六进制（适配 char(64)），实际长度 %d", len(a))
	}
	if f.Hash("13800138001") == a {
		t.Fatal("不同明文的摘要不应相同")
	}
}

// 换密钥后摘要必须变化，证明用的是 HMAC 而非裸哈希——
// 裸 SHA256 可被彩虹表穷举反推手机号。
func TestHashIsKeyed(t *testing.T) {
	f1 := newField(t)
	f2, err := NewSensitiveField(encKey, []byte("a-completely-different-hmac-key!!"))
	if err != nil {
		t.Fatal(err)
	}
	if f1.Hash("13800138000") == f2.Hash("13800138000") {
		t.Fatal("不同 hmacKey 应产出不同摘要，否则说明未使用密钥")
	}
}

func TestDecryptRejectsTamperedCiphertext(t *testing.T) {
	f := newField(t)
	ct, err := f.Encrypt("13800138000")
	if err != nil {
		t.Fatal(err)
	}
	// 破坏 base64 中间一个字符。
	b := []byte(ct)
	if b[len(b)/2] == 'A' {
		b[len(b)/2] = 'B'
	} else {
		b[len(b)/2] = 'A'
	}
	if _, err := f.Decrypt(string(b)); err == nil {
		t.Fatal("篡改密文应被 GCM 完整性校验拒绝")
	}
}

func TestDecryptRejectsGarbage(t *testing.T) {
	f := newField(t)
	for _, bad := range []string{"not-base64!!!", "YQ", "AAAA"} {
		if _, err := f.Decrypt(bad); err == nil {
			t.Errorf("非法密文 %q 应报错", bad)
		}
	}
}

func TestEmptyInputPassesThrough(t *testing.T) {
	f := newField(t)
	ct, err := f.Encrypt("")
	if err != nil || ct != "" {
		t.Fatalf("空明文应返回空串: %q %v", ct, err)
	}
	pt, err := f.Decrypt("")
	if err != nil || pt != "" {
		t.Fatalf("空密文应返回空串: %q %v", pt, err)
	}
	if f.Hash("") != "" {
		t.Fatal("空明文的摘要应为空串")
	}
}

// 用另一把加密密钥解不出来，确认密钥确实参与了加密。
func TestDecryptWithWrongKeyFails(t *testing.T) {
	f1 := newField(t)
	ct, err := f1.Encrypt("13800138000")
	if err != nil {
		t.Fatal(err)
	}
	f2, err := NewSensitiveField([]byte("ffffffffffffffffffffffffffffffff"), hmacKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f2.Decrypt(ct); err == nil {
		t.Fatal("错误密钥应无法解密")
	}
}

func TestMaskPhone(t *testing.T) {
	cases := map[string]string{
		"13800138000": "138****8000",
		"":            "",
		"123":         "****",
		"1234567":     "123****567",
	}
	for in, want := range cases {
		if got := MaskPhone(in); got != want {
			t.Errorf("MaskPhone(%q) = %q, want %q", in, got, want)
		}
	}
	// 脱敏后不得残留完整号码。
	if strings.Contains(MaskPhone("13800138000"), "0013") {
		t.Fatal("脱敏不彻底")
	}
}

func TestMaskEmail(t *testing.T) {
	cases := map[string]string{
		"zhangsan@stu.pku.edu.cn": "zh***@stu.pku.edu.cn",
		"ab@x.com":                "***@x.com",
		"a@x.com":                 "***@x.com",
		"noatsign":                "****",
		"":                        "****",
	}
	for in, want := range cases {
		if got := MaskEmail(in); got != want {
			t.Errorf("MaskEmail(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMaskTail(t *testing.T) {
	if got := MaskTail("2021010101", 4); got != "2021******" {
		t.Fatalf("MaskTail = %q", got)
	}
	if got := MaskTail("abc", 10); got != "abc" {
		t.Fatalf("keep 超长时应原样返回，实际 %q", got)
	}
	if got := MaskTail("abc", -1); got != "***" {
		t.Fatalf("负数 keep 应视为 0，实际 %q", got)
	}
}
