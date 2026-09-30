package data

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/fleamarket/app/user/internal/biz"
	"github.com/fleamarket/app/user/internal/conf"

	"github.com/fleamarket/pkg/cache"
	"github.com/fleamarket/pkg/errs"
)

// 本文件实现三个基于 Redis 的短期状态存储：
// 验证码、令牌吊销名单、邮箱认证一次性令牌。
//
// 共同特点是都需要原子的「校验 + 失效」语义，因此关键操作用 Lua 脚本完成。
// 若拆成两条命令，并发请求会在两条命令之间穿插，导致同一凭证被消费多次。

// --- 验证码 ---

// consumeCodeScript 原子地校验并删除验证码。
//
// 必须原子：若先 GET 比对、再 DEL，两个并发请求可能都读到正确的码并都通过校验，
// 同一条验证码就被用了两次。
const consumeCodeScript = `
if redis.call("GET", KEYS[1]) == ARGV[1] then
	redis.call("DEL", KEYS[1])
	return 1
end
return 0
`

// acquireSendQuotaScript 一次性完成频率限制与每日上限的判定。
//
// 返回值约定：
//
//	> 0  被频率限制拦下，值为剩余等待秒数
//	0    允许发送
//	-1   超出每日上限
//
// 之所以合成一个脚本：两项限制若分成两次往返，
// 会出现「通过了频率检查但在写每日计数前被另一请求抢先」的窗口。
const acquireSendQuotaScript = `
local ttl = redis.call("TTL", KEYS[1])
if ttl > 0 then
	return ttl
end
local daily = redis.call("INCR", KEYS[2])
if daily == 1 then
	redis.call("EXPIRE", KEYS[2], ARGV[3])
end
if daily > tonumber(ARGV[2]) then
	return -1
end
redis.call("SET", KEYS[1], "1", "EX", ARGV[1])
return 0
`

type verifyCodeStore struct {
	c              cache.Cacher
	resendInterval time.Duration
	dailyLimit     int64
}

// NewVerifyCodeStore 构造验证码存储。
func NewVerifyCodeStore(data *Data, c *conf.Sms) biz.VerifyCodeStore {
	resendInterval := c.GetResendInterval().AsDuration()
	if resendInterval <= 0 {
		resendInterval = 60 * time.Second
	}
	dailyLimit := c.GetDailyLimit()
	if dailyLimit <= 0 {
		dailyLimit = 10
	}
	return &verifyCodeStore{
		c:              data.Cache,
		resendInterval: resendInterval,
		dailyLimit:     int64(dailyLimit),
	}
}

func codeKey(scene, target string) string { return "vc:code:" + scene + ":" + target }
func sendKey(scene, target string) string { return "vc:send:" + scene + ":" + target }
func failKey(scene, target string) string { return "vc:fail:" + scene + ":" + target }

// dailyKey 每日计数的 key 带日期后缀。
//
// 用 key 过期而非定时清零：跨天的边界由 Redis 的 TTL 自然处理，
// 不需要额外的定时任务，也不存在「清零任务挂了导致用户被永久限制」的风险。
func dailyKey(scene, target string) string {
	return "vc:daily:" + scene + ":" + target + ":" + time.Now().Format("20060102")
}

func (s *verifyCodeStore) Save(ctx context.Context, scene, target, code string, ttl time.Duration) error {
	if err := s.c.Set(ctx, codeKey(scene, target), []byte(code), ttl); err != nil {
		return errs.Internal("验证码存储失败")
	}
	return nil
}

func (s *verifyCodeStore) Consume(ctx context.Context, scene, target, code string) (bool, error) {
	if code == "" {
		return false, nil
	}
	res, err := s.c.Eval(ctx, consumeCodeScript, []string{codeKey(scene, target)}, code)
	if err != nil {
		return false, errs.Internal("验证码校验失败")
	}
	n, _ := res.(int64)
	return n == 1, nil
}

func (s *verifyCodeStore) AcquireSendQuota(ctx context.Context, scene, target string) (int, error) {
	res, err := s.c.Eval(ctx, acquireSendQuotaScript,
		[]string{sendKey(scene, target), dailyKey(scene, target)},
		int64(s.resendInterval.Seconds()), s.dailyLimit, int64(86400))
	if err != nil {
		return 0, errs.Internal("发送频率校验失败")
	}
	n, _ := res.(int64)
	switch {
	case n == -1:
		return 0, errs.RateLimited("今日获取验证码次数已达上限，请明天再试")
	case n > 0:
		return int(n), nil
	default:
		return 0, nil
	}
}

// CountFailures 递增并返回失败次数。
//
// TTL 与验证码有效期一致：验证码过期后重新获取即可解除锁定，
// 不需要让用户等一个额外的惩罚期。
func (s *verifyCodeStore) CountFailures(ctx context.Context, scene, target string) (int64, error) {
	key := failKey(scene, target)
	n, err := s.c.Incr(ctx, key, 1)
	if err != nil {
		return 0, errs.Internal("失败计数写入失败")
	}
	if n == 1 {
		if err := s.c.Expire(ctx, key, 5*time.Minute); err != nil {
			return n, nil
		}
	}
	return n, nil
}

func (s *verifyCodeStore) ClearFailures(ctx context.Context, scene, target string) error {
	return s.c.Del(ctx, failKey(scene, target))
}

// --- 令牌吊销名单 ---

type tokenStore struct {
	c cache.Cacher
}

// NewTokenStore 构造吊销名单。
func NewTokenStore(data *Data) biz.TokenStore { return &tokenStore{c: data.Cache} }

func revokeKey(tokenID string) string { return "revoked:" + tokenID }

// Revoke 把令牌加入吊销名单。
//
// ttl 传入令牌的剩余有效期：超过有效期后令牌本身就失效了，
// 名单里继续留着只是白占内存。这让吊销名单的大小与「在线用户数」
// 同量级，而不是随时间无限增长。
func (s *tokenStore) Revoke(ctx context.Context, tokenID string, ttl time.Duration) error {
	if tokenID == "" || ttl <= 0 {
		return nil
	}
	if err := s.c.Set(ctx, revokeKey(tokenID), []byte("1"), ttl); err != nil {
		return errs.Internal("吊销令牌失败")
	}
	return nil
}

func (s *tokenStore) IsRevoked(ctx context.Context, tokenID string) (bool, error) {
	if tokenID == "" {
		return false, nil
	}
	_, err := s.c.Get(ctx, revokeKey(tokenID))
	switch {
	case err == nil:
		return true, nil
	case isCacheMiss(err):
		return false, nil
	default:
		// 返回错误而非 false，让上层决定降级策略。
		// pkg/auth 的选择是放行并记录告警（见其注释）。
		return false, err
	}
}

// --- 邮箱认证一次性令牌 ---

type emailAuthTokenStore struct {
	c cache.Cacher
}

// NewEmailAuthTokenStore 构造存储。
func NewEmailAuthTokenStore(data *Data) biz.EmailAuthTokenStore {
	return &emailAuthTokenStore{c: data.Cache}
}

func emailTokenKey(token string) string { return "eauth:" + token }

// consumeEmailTokenScript 原子地读出并删除令牌。
//
// 与验证码同理：认证链接被重复点击（用户手滑、邮件客户端预取）时，
// 必须保证只有一次生效。
const consumeEmailTokenScript = `
local v = redis.call("GET", KEYS[1])
if v then
	redis.call("DEL", KEYS[1])
	return v
end
return false
`

func (s *emailAuthTokenStore) Save(ctx context.Context, token string,
	authID int64, ttl time.Duration) error {

	val := []byte(strconv.FormatInt(authID, 10))
	if err := s.c.Set(ctx, emailTokenKey(token), val, ttl); err != nil {
		return errs.Internal("认证令牌存储失败")
	}
	return nil
}

func (s *emailAuthTokenStore) Consume(ctx context.Context, token string) (int64, bool, error) {
	if token == "" {
		return 0, false, nil
	}
	res, err := s.c.Eval(ctx, consumeEmailTokenScript, []string{emailTokenKey(token)})
	if err != nil {
		return 0, false, errs.Internal("认证令牌校验失败")
	}
	str, ok := res.(string)
	if !ok || str == "" {
		return 0, false, nil
	}
	id, err := strconv.ParseInt(str, 10, 64)
	if err != nil {
		return 0, false, fmt.Errorf("data: malformed email auth token value %q: %w", str, err)
	}
	return id, true, nil
}

// isCacheMiss 判断是否为「键不存在」。
func isCacheMiss(err error) bool {
	return errors.Is(err, cache.ErrNotFound)
}
