package data

import (
	"log/slog"

	"github.com/fleamarket/app/user/internal/biz"
	"github.com/fleamarket/app/user/internal/conf"

	"github.com/fleamarket/pkg/auth"
	"github.com/fleamarket/pkg/crypto"
	"github.com/fleamarket/pkg/idgen"
	"github.com/fleamarket/pkg/storage"
)

// 本文件集中提供基础设施组件的 wire provider。
//
// 放在 data 层是因为它们都需要读配置：biz 层不依赖 conf，
// 由 data 完成「配置 → 组件」的转换后注入给 biz。

// NewSensitiveField 构造敏感字段加解密器。
//
// 密钥缺失时直接返回错误让服务起不来，而不是回落到某个默认值。
// 手机号加密是合规要求，用固定的弱默认密钥比不加密更危险——
// 它会造成「已加密」的错觉。
func NewSensitiveField(c *conf.Crypto) (*crypto.SensitiveField, error) {
	return crypto.NewSensitiveField(
		[]byte(c.GetFieldEncKey()),
		[]byte(c.GetFieldHmacKey()),
	)
}

// NewStorage 构造对象存储客户端。
func NewStorage(c *conf.Storage) (storage.Storage, error) {
	return storage.NewS3(storage.S3Config{
		Endpoint:        c.GetEndpoint(),
		AccessKey:       c.GetAccessKey(),
		SecretKey:       c.GetSecretKey(),
		UseSSL:          c.GetUseSsl(),
		Region:          c.GetRegion(),
		Bucket:          c.GetBucket(),
		PublicBaseURL:   c.GetPublicBaseUrl(),
		ProcessQueryKey: c.GetProcessQueryKey(),
	})
}

// NewIDGenerator 构造雪花 ID 生成器。
//
// worker_id 必须在集群内唯一，否则会产生重复 ID。
// 这里不自动分配（那需要依赖 etcd 顺序租约），而是要求显式配置——
// 让部署时的疏漏在启动阶段就以「ID 重复」之外的方式暴露不现实，
// 因此至少保证越界的配置会被拒绝。
func NewIDGenerator(c *conf.Business) (*idgen.Generator, error) {
	return idgen.New(int64(c.GetWorkerId()))
}

// NewJWTManager 构造 JWT 管理器。
//
// 关键接线：TokenStore 必须作为 Revoker 传入，否则 Verify 不会查吊销名单，
// 登出接口会「成功返回但令牌仍可用」。这是一个静默失效的陷阱，
// 由 biz 层的 TestLogoutRevokesRefreshToken 守住。
func NewJWTManager(c *conf.Auth, revoker biz.TokenStore) (*auth.Manager, error) {
	return auth.NewManager(auth.Config{
		Secret:     c.GetJwtSecret(),
		Issuer:     "fleamarket",
		AccessTTL:  c.GetAccessTtl().AsDuration(),
		RefreshTTL: c.GetRefreshTtl().AsDuration(),
	}, revoker)
}

// NewCampusAuthConfig 把业务配置转为 biz 层的具名配置结构。
func NewCampusAuthConfig(c *conf.Business) biz.CampusAuthConfig {
	return biz.CampusAuthConfig{VerifyBaseURL: c.GetCampusAuthVerifyUrl()}
}

// NewEventPublisherProvider 是 NewEventPublisher 的 wire 适配。
//
// 单独包一层是因为 NewEventPublisher 需要 namespace 字符串，
// 而 wire 无法为裸 string 类型的依赖选择来源。
func NewEventPublisherProvider(data *Data, c *conf.Business,
	logger *slog.Logger) (biz.EventPublisher, func(), error) {

	return NewEventPublisher(data, c.GetMqNamespace(), logger)
}
