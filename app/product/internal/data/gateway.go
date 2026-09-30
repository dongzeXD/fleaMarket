package data

import (
	"context"
	"log/slog"

	"github.com/fleamarket/app/product/internal/biz"
	"github.com/fleamarket/app/product/internal/conf"

	userv1 "github.com/fleamarket/api/user/v1"
	"github.com/fleamarket/pkg/idgen"
	"github.com/fleamarket/pkg/mq"
	pkgregistry "github.com/fleamarket/pkg/registry"
	"github.com/fleamarket/pkg/storage"
	"github.com/fleamarket/pkg/transport"
	kratosregistry "github.com/go-kratos/kratos/v3/registry"
	"google.golang.org/grpc"
)

// 本文件提供基础设施组件与跨服务网关的 wire provider。

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
func NewIDGenerator(c *conf.Business) (*idgen.Generator, error) {
	return idgen.New(int64(c.GetWorkerId()))
}

// UserConn 到 user-service 的 gRPC 连接。
type UserConn struct {
	conn *grpc.ClientConn
}

// NewUserConn 建立到 user-service 的连接。
//
// GetSellerInfo 是纯查询，列入幂等清单允许重试：
// 发布商品的前置校验若因一次网络抖动失败，用户会看到莫名的「暂时无法校验资格」。
func NewUserConn(d kratosregistry.Discovery, c *conf.Downstream) (*UserConn, func(), error) {
	svc := c.GetUser()
	name := svc.GetName()
	if name == "" {
		name = "user-service"
	}
	conn, cleanup, err := transport.NewGRPCConn(context.Background(), transport.ClientOptions{
		Endpoint:  pkgregistry.Endpoint(name),
		Discovery: d,
		Timeout:   svc.GetTimeout().AsDuration(),
		IdempotentOperations: []string{
			"/user.v1.UserService/GetSellerInfo",
			"/user.v1.UserService/BatchGetUserBrief",
		},
	})
	if err != nil {
		return nil, nil, err
	}
	return &UserConn{conn: conn}, cleanup, nil
}

// userGateway 通过 gRPC 调用 user-service。
type userGateway struct {
	client userv1.UserServiceClient
	log    *slog.Logger
}

// NewUserGateway 构造网关。
func NewUserGateway(c *UserConn, logger *slog.Logger) biz.UserGateway {
	return &userGateway{
		client: userv1.NewUserServiceClient(c.conn),
		log:    logger.With(slog.String("gateway", "user")),
	}
}

func (g *userGateway) GetSellerInfo(ctx context.Context, userID int64) (*biz.SellerInfo, error) {
	reply, err := g.client.GetSellerInfo(ctx, &userv1.GetSellerInfoRequest{UserId: userID})
	if err != nil {
		return nil, err
	}
	return &biz.SellerInfo{
		UserID:         reply.GetUserId(),
		CampusVerified: reply.GetCampusVerified(),
		SchoolID:       reply.GetSchoolId(),
		CampusID:       reply.GetCampusId(),
		Status:         uint8(reply.GetStatus()),
	}, nil
}

// moderationGateway 内容审核网关。
//
// review-service 尚未实现（Task 13），当前提供一个显式的「转人工」实现：
// 返回 immediate=false 使商品停留在待审核状态，等人工或后续接入的
// review-service 经 MQ 回写结论。
//
// 为什么不临时放行：默认通过会让审核这道闸门在整个开发期形同虚设，
// 而「停在待审核」是个可见的、不会造成合规风险的状态。
type moderationGateway struct {
	log *slog.Logger
}

// NewModerationGateway 构造审核网关。
func NewModerationGateway(logger *slog.Logger) biz.ModerationGateway {
	return &moderationGateway{log: logger.With(slog.String("gateway", "review"))}
}

func (g *moderationGateway) SubmitProduct(ctx context.Context, productID int64,
	_, _ string) (bool, bool, string, []string, error) {

	g.log.InfoContext(ctx, "review-service 尚未接入，商品保持待审核",
		slog.Int64("product_id", productID))
	return false, false, "", nil, nil
}

// eventPublisher 事件发布。
type eventPublisher struct {
	producer mq.Producer
}

// NewEventPublisher 构造事件发布器。
func NewEventPublisher(data *Data, c *conf.Business) (biz.EventPublisher, func(), error) {
	producer, err := mq.NewRedisStreamProducer(mq.RedisStreamConfig{
		Client:    data.Cache.Client(),
		Namespace: c.GetMqNamespace(),
	})
	if err != nil {
		return nil, nil, err
	}
	return &eventPublisher{producer: producer}, func() { _ = producer.Close() }, nil
}

func (p *eventPublisher) Publish(ctx context.Context, topic string, payload []byte) error {
	_, err := p.producer.Publish(ctx, topic, payload, nil)
	return err
}
