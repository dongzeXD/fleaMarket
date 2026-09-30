package data

import (
	"context"
	"time"

	"github.com/fleamarket/app/trade/internal/biz"
	"github.com/fleamarket/app/trade/internal/conf"

	productv1 "github.com/fleamarket/api/product/v1"
	userv1 "github.com/fleamarket/api/user/v1"
	"github.com/fleamarket/pkg/cache"
	"github.com/fleamarket/pkg/idgen"
	"github.com/fleamarket/pkg/mq"
	pkgregistry "github.com/fleamarket/pkg/registry"
	"github.com/fleamarket/pkg/transport"
	kratosregistry "github.com/go-kratos/kratos/v3/registry"
	"google.golang.org/grpc"
)

// NewIDGenerator 构造雪花 ID 生成器。
func NewIDGenerator(c *conf.Business) (*idgen.Generator, error) {
	return idgen.New(int64(c.GetWorkerId()))
}

// --- product-service 网关 ---

// ProductConn 到 product-service 的连接。
type ProductConn struct{ conn *grpc.ClientConn }

// NewProductConn 建立连接。
//
// 注意：LockForOrder / ReleaseOrderLock / MarkSold 都不列入幂等重试清单。
// 它们改变商品状态，重试可能造成二次副作用——例如释放锁的请求重试时，
// 商品可能已被另一个订单重新锁定，重试会误释放。这类操作的正确性
// 由各方法自身的条件更新保证，不靠传输层重试。
func NewProductConn(d kratosregistry.Discovery, c *conf.Downstream) (*ProductConn, func(), error) {
	svc := c.GetProduct()
	name := svc.GetName()
	if name == "" {
		name = "product-service"
	}
	conn, cleanup, err := transport.NewGRPCConn(context.Background(), transport.ClientOptions{
		Endpoint:  pkgregistry.Endpoint(name),
		Discovery: d,
		Timeout:   svc.GetTimeout().AsDuration(),
	})
	if err != nil {
		return nil, nil, err
	}
	return &ProductConn{conn: conn}, cleanup, nil
}

type productGateway struct {
	client productv1.ProductServiceClient
}

// NewProductGateway 构造网关。
func NewProductGateway(c *ProductConn) biz.ProductGateway {
	return &productGateway{client: productv1.NewProductServiceClient(c.conn)}
}

func (g *productGateway) LockForOrder(ctx context.Context, productID, orderID, buyerID int64) (*biz.LockResult, error) {
	reply, err := g.client.LockForOrder(ctx, &productv1.LockForOrderRequest{
		ProductId: productID,
		OrderId:   orderID,
		BuyerId:   buyerID,
	})
	if err != nil {
		return nil, err
	}
	return &biz.LockResult{
		Locked:        reply.GetLocked(),
		CurrentStatus: uint8(reply.GetCurrentStatus()),
		PriceCents:    reply.GetPriceCents(),
		SellerID:      reply.GetSellerId(),
	}, nil
}

func (g *productGateway) ReleaseOrderLock(ctx context.Context, productID, orderID int64) (bool, error) {
	reply, err := g.client.ReleaseOrderLock(ctx, &productv1.ReleaseOrderLockRequest{
		ProductId: productID,
		OrderId:   orderID,
	})
	if err != nil {
		return false, err
	}
	return reply.GetReleased(), nil
}

func (g *productGateway) MarkSold(ctx context.Context, productID, orderID int64) (bool, error) {
	reply, err := g.client.MarkSold(ctx, &productv1.MarkSoldRequest{
		ProductId: productID,
		OrderId:   orderID,
	})
	if err != nil {
		return false, err
	}
	return reply.GetMarked(), nil
}

// --- user-service 网关 ---

// UserConn 到 user-service 的连接。
type UserConn struct{ conn *grpc.ClientConn }

// NewUserConn 建立连接。
//
// AdjustCredit 本身幂等（幂等键为 reason+refID），因此列入重试清单是安全的：
// 重试同一次调整不会重复加减分。
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
			"/user.v1.UserService/AdjustCredit",
		},
	})
	if err != nil {
		return nil, nil, err
	}
	return &UserConn{conn: conn}, cleanup, nil
}

type userGateway struct {
	client userv1.UserServiceClient
}

// NewUserGateway 构造网关。
func NewUserGateway(c *UserConn) biz.UserGateway {
	return &userGateway{client: userv1.NewUserServiceClient(c.conn)}
}

func (g *userGateway) AdjustCredit(ctx context.Context, userID int64, reason uint8, refID int64) error {
	_, err := g.client.AdjustCredit(ctx, &userv1.AdjustCreditRequest{
		UserId: userID,
		Reason: userv1.CreditReason(reason),
		RefId:  refID,
	})
	return err
}

// --- 分布式锁 ---

// NewLocker 从缓存构造分布式锁。
func NewLocker(data *Data) biz.Locker {
	return &lockerAdapter{cache: data.Cache}
}

// lockerAdapter 把 pkg/cache.Locker 适配成 biz.Locker。
//
// 中间加一层是为了让 biz 不依赖 pkg/cache 的类型——
// biz.Locker 只暴露它真正需要的 Lock 方法。
type lockerAdapter struct {
	cache *cache.RedisCache
}

func (l *lockerAdapter) Lock(ctx context.Context, key string, ttl, wait time.Duration) (func(context.Context) error, error) {
	unlock, err := l.cache.Lock(ctx, key, ttl, wait)
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context) error { return unlock(ctx) }, nil
}

// --- 事件发布 ---

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
