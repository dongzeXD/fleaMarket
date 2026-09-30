// Package auth 提供 JWT 的签发、校验与 kratos 中间件接入。
//
// 职责划分（doc.md §13）：
//   - api-gateway 校验 JWT，把身份信息注入 gRPC metadata；
//   - 下游服务从 metadata 读取身份，不重复解 JWT；
//   - 但下游仍须在 biz 层校验资源归属，不把「网关一定校验过」当作安全前提。
//
// 泛用性约束：本包只认识「身份」这一通用概念（用户 ID、角色、租户），
// 不知道校区、信誉分之类的业务属性。需要携带业务属性的场景用 Extra 字段。
package auth

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// 错误定义。区分这三种是必要的：过期应提示客户端刷新，
// 而签名无效意味着令牌被伪造，应记录安全事件。
var (
	// ErrTokenMissing 请求未携带令牌。
	ErrTokenMissing = errors.New("auth: token missing")
	// ErrTokenInvalid 令牌格式错误或签名校验失败。
	ErrTokenInvalid = errors.New("auth: token invalid")
	// ErrTokenExpired 令牌已过期。
	ErrTokenExpired = errors.New("auth: token expired")
	// ErrTokenRevoked 令牌已被吊销。
	ErrTokenRevoked = errors.New("auth: token revoked")
)

// TokenType 令牌类型。
//
// 必须区分并在校验时核对：若不区分，攻击者可用长效的 refresh_token
// 直接当 access_token 访问业务接口，refresh 机制的意义就消失了。
type TokenType string

const (
	// TypeAccess 访问令牌，短期，用于业务接口。
	TypeAccess TokenType = "access"
	// TypeRefresh 刷新令牌，长期，仅用于换取新的 access_token。
	TypeRefresh TokenType = "refresh"
)

// Claims 令牌载荷。
type Claims struct {
	// UserID 主体标识。
	UserID int64
	// Role 角色，如 "user" / "admin"。
	Role string
	// Type 令牌类型。
	Type TokenType
	// TokenID 本令牌的唯一标识，用于吊销。
	TokenID string
	// Extra 业务自定义的附加属性。
	//
	// 用 map 而非具体字段，是为了让公共库不认识业务概念：
	// user-service 往里放 campus_id，公共库只负责原样签发与还原。
	Extra map[string]string

	IssuedAt  time.Time
	ExpiresAt time.Time
}

// registeredClaims 是实际参与签名的结构。
type registeredClaims struct {
	jwt.RegisteredClaims
	UserID int64             `json:"uid"`
	Role   string            `json:"rol,omitempty"`
	Type   string            `json:"typ"`
	Extra  map[string]string `json:"ext,omitempty"`
}

// Config 签发器配置。
type Config struct {
	// Secret 签名密钥。必须由环境变量或密钥管理服务注入。
	//
	// 使用 HMAC（对称）而非 RSA：本项目的令牌只由 user-service 签发、
	// 由网关校验，二者同属一个信任域，不存在「校验方不应拥有签发能力」的需求。
	// 对称算法的性能与运维复杂度都更优。
	Secret string
	// Issuer 签发方标识，写入 iss。
	Issuer string
	// AccessTTL 访问令牌有效期，默认 2 小时。
	AccessTTL time.Duration
	// RefreshTTL 刷新令牌有效期，默认 30 天。
	RefreshTTL time.Duration
}

func (c *Config) setDefaults() error {
	if c.Secret == "" {
		return errors.New("auth: empty jwt secret")
	}
	if len(c.Secret) < 32 {
		// 密钥过短会让 HMAC 易被暴力破解。这里直接拒绝而非警告，
		// 因为「配置写错了」在启动时暴露的代价远低于上线后。
		return fmt.Errorf("auth: jwt secret too short (%d bytes), need at least 32", len(c.Secret))
	}
	// 0 表示「未设置，用默认值」；负数则明确是配置写错了，
	// 静默改成默认值会掩盖错误，直接拒绝更安全。
	if c.AccessTTL < 0 || c.RefreshTTL < 0 {
		return errors.New("auth: TTL must not be negative")
	}
	if c.AccessTTL == 0 {
		c.AccessTTL = 2 * time.Hour
	}
	if c.RefreshTTL == 0 {
		c.RefreshTTL = 30 * 24 * time.Hour
	}
	if c.Issuer == "" {
		c.Issuer = "fleamarket"
	}
	return nil
}

// Revoker 判断令牌是否已被吊销。
//
// 定义为接口由调用方实现（通常基于 Redis）：吊销名单的存储是业务侧的选择，
// 公共库不应绑定某个存储。传 nil 表示不做吊销检查。
type Revoker interface {
	IsRevoked(ctx context.Context, tokenID string) (bool, error)
}

// Manager JWT 签发与校验器。
type Manager struct {
	cfg     Config
	revoker Revoker
}

// NewManager 创建管理器。
func NewManager(c Config, revoker Revoker) (*Manager, error) {
	if err := c.setDefaults(); err != nil {
		return nil, err
	}
	return &Manager{cfg: c, revoker: revoker}, nil
}

// TokenPair 一对令牌。
type TokenPair struct {
	AccessToken  string
	RefreshToken string
	// AccessExpiresIn access_token 剩余有效秒数，供客户端安排刷新。
	AccessExpiresIn int
	// RefreshTokenID refresh 令牌的 ID，供业务侧记录以便日后吊销。
	RefreshTokenID string
}

// Issue 签发一对令牌。
func (m *Manager) Issue(userID int64, role string, extra map[string]string,
	newID func() string) (*TokenPair, error) {

	if userID <= 0 {
		return nil, errors.New("auth: invalid user id")
	}
	if newID == nil {
		return nil, errors.New("auth: nil id generator")
	}

	now := time.Now()
	accessID, refreshID := newID(), newID()

	access, err := m.sign(userID, role, TypeAccess, accessID, extra, now, m.cfg.AccessTTL)
	if err != nil {
		return nil, err
	}
	// refresh 令牌不携带 extra：它唯一的用途是换取新的 access_token，
	// 届时业务属性会从数据库重新读取。放进去只会让过期的属性被延续。
	refresh, err := m.sign(userID, role, TypeRefresh, refreshID, nil, now, m.cfg.RefreshTTL)
	if err != nil {
		return nil, err
	}

	return &TokenPair{
		AccessToken:     access,
		RefreshToken:    refresh,
		AccessExpiresIn: int(m.cfg.AccessTTL.Seconds()),
		RefreshTokenID:  refreshID,
	}, nil
}

func (m *Manager) sign(userID int64, role string, typ TokenType, tokenID string,
	extra map[string]string, now time.Time, ttl time.Duration) (string, error) {

	claims := registeredClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    m.cfg.Issuer,
			Subject:   strconv.FormatInt(userID, 10),
			ID:        tokenID,
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
		},
		UserID: userID,
		Role:   role,
		Type:   string(typ),
		Extra:  extra,
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	s, err := tok.SignedString([]byte(m.cfg.Secret))
	if err != nil {
		return "", fmt.Errorf("auth: sign token: %w", err)
	}
	return s, nil
}

// Verify 校验令牌并返回载荷。
//
// want 指定期望的令牌类型，不匹配时返回 ErrTokenInvalid——
// 这是防止 refresh_token 被当作 access_token 使用的关键一步。
func (m *Manager) Verify(ctx context.Context, token string, want TokenType) (*Claims, error) {
	if token == "" {
		return nil, ErrTokenMissing
	}

	var rc registeredClaims
	parsed, err := jwt.ParseWithClaims(token, &rc, func(t *jwt.Token) (any, error) {
		// 显式核对算法，拒绝 alg=none 与算法混淆攻击。
		if t.Method.Alg() != jwt.SigningMethodHS256.Alg() {
			return nil, fmt.Errorf("unexpected signing method %q", t.Method.Alg())
		}
		return []byte(m.cfg.Secret), nil
	}, jwt.WithIssuer(m.cfg.Issuer), jwt.WithExpirationRequired())

	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return nil, ErrTokenExpired
		}
		return nil, ErrTokenInvalid
	}
	if !parsed.Valid {
		return nil, ErrTokenInvalid
	}
	if rc.Type != string(want) {
		return nil, ErrTokenInvalid
	}
	if rc.UserID <= 0 {
		return nil, ErrTokenInvalid
	}

	if m.revoker != nil && rc.ID != "" {
		revoked, err := m.revoker.IsRevoked(ctx, rc.ID)
		if err != nil {
			// 吊销名单不可用时的取舍：放行而非拒绝。
			// 拒绝会让 Redis 故障升级为全站无法登录；
			// 放行的风险窗口仅限于已登出用户的令牌剩余有效期。
			// 调用方应对此记录告警。
			return claimsFrom(&rc), nil
		}
		if revoked {
			return nil, ErrTokenRevoked
		}
	}

	return claimsFrom(&rc), nil
}

func claimsFrom(rc *registeredClaims) *Claims {
	c := &Claims{
		UserID:  rc.UserID,
		Role:    rc.Role,
		Type:    TokenType(rc.Type),
		TokenID: rc.ID,
		Extra:   rc.Extra,
	}
	if rc.IssuedAt != nil {
		c.IssuedAt = rc.IssuedAt.Time
	}
	if rc.ExpiresAt != nil {
		c.ExpiresAt = rc.ExpiresAt.Time
	}
	return c
}

// ParseUnverified 不校验签名地读出载荷。
//
// 仅用于日志与排查（如记录「哪个用户的令牌过期了」），
// 绝不可用于任何鉴权判断。命名中的 Unverified 是刻意的警示。
func ParseUnverified(token string) (*Claims, error) {
	var rc registeredClaims
	if _, _, err := jwt.NewParser().ParseUnverified(token, &rc); err != nil {
		return nil, ErrTokenInvalid
	}
	return claimsFrom(&rc), nil
}
