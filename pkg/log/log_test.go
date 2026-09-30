package log

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

// 脱敏是安全属性，必须有测试守住：一旦 SensitiveKeys 被误删或 filter 未生效，
// 手机号与令牌就会明文落盘，属于不可挽回的事故。
func TestSensitiveFieldsAreRedacted(t *testing.T) {
	var buf bytes.Buffer
	l := New(Config{
		ServiceName: "user-service",
		Format:      FormatJSON,
		Writer:      &buf,
	})

	l.Info("login attempt",
		slog.String("phone", "13800138000"),
		slog.String("access_token", "eyJhbGciOiJIUzI1NiJ9.secret"),
		slog.String("sms_code", "123456"),
		slog.String("student_no", "2021010101"),
		slog.String("Authorization", "Bearer abcdef"),
		slog.String("user_id", "10086"), // 非敏感，应保留
	)

	out := buf.String()
	for _, leak := range []string{
		"13800138000",
		"eyJhbGciOiJIUzI1NiJ9.secret",
		"123456",
		"2021010101",
		"Bearer abcdef",
	} {
		if strings.Contains(out, leak) {
			t.Errorf("敏感值泄漏到日志: %q\n完整输出: %s", leak, out)
		}
	}
	if !strings.Contains(out, "10086") {
		t.Errorf("非敏感字段被误脱敏，输出: %s", out)
	}
}

func TestExtraSensitiveKeys(t *testing.T) {
	var buf bytes.Buffer
	l := New(Config{
		Format:             FormatJSON,
		Writer:             &buf,
		ExtraSensitiveKeys: []string{"bank_card"},
	})
	l.Info("test", slog.String("bank_card", "6222021234567890"))
	if strings.Contains(buf.String(), "6222021234567890") {
		t.Errorf("业务追加的敏感字段未生效: %s", buf.String())
	}
}

func TestServiceAttrsArePresent(t *testing.T) {
	var buf bytes.Buffer
	l := New(Config{
		ServiceID:      "host-1",
		ServiceName:    "product-service",
		ServiceVersion: "v1.2.3",
		Format:         FormatJSON,
		Writer:         &buf,
	})
	l.Info("hello")

	out := buf.String()
	for _, want := range []string{"host-1", "product-service", "v1.2.3"} {
		if !strings.Contains(out, want) {
			t.Errorf("日志缺少服务标识 %q，输出: %s", want, out)
		}
	}
}

func TestLevelFiltering(t *testing.T) {
	var buf bytes.Buffer
	l := New(Config{Level: "warn", Format: FormatJSON, Writer: &buf})

	l.Debug("debug-msg")
	l.Info("info-msg")
	l.Warn("warn-msg")
	l.Error("error-msg")

	out := buf.String()
	if strings.Contains(out, "debug-msg") || strings.Contains(out, "info-msg") {
		t.Errorf("低于 warn 的日志应被丢弃: %s", out)
	}
	if !strings.Contains(out, "warn-msg") || !strings.Contains(out, "error-msg") {
		t.Errorf("warn 及以上日志应保留: %s", out)
	}
}

func TestEmptyLevelDefaultsToInfo(t *testing.T) {
	var buf bytes.Buffer
	l := New(Config{Format: FormatJSON, Writer: &buf})
	l.Debug("debug-msg")
	l.Info("info-msg")
	out := buf.String()
	if strings.Contains(out, "debug-msg") {
		t.Errorf("默认级别应为 info，debug 不应输出: %s", out)
	}
	if !strings.Contains(out, "info-msg") {
		t.Errorf("info 应输出: %s", out)
	}
}
