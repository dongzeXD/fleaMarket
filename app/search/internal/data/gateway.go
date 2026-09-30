package data

import (
	"context"
	"log/slog"
	"strconv"
	"time"

	"github.com/fleamarket/app/search/internal/biz"
	"github.com/fleamarket/app/search/internal/conf"
	"github.com/fleamarket/app/search/internal/engine"

	productv1 "github.com/fleamarket/api/product/v1"
	"github.com/fleamarket/pkg/cache"
	pkgregistry "github.com/fleamarket/pkg/registry"
	"github.com/fleamarket/pkg/transport"
	kratosregistry "github.com/go-kratos/kratos/v3/registry"
	"google.golang.org/grpc"
)

// ProductConn 到 product-service 的 gRPC 连接。
type ProductConn struct {
	conn *grpc.ClientConn
}

// NewProductConn 建立连接。
//
// 三个方法都列入幂等清单：它们全是查询，且都在故障恢复路径上
// （索引重建、降级检索、结果校正），重试的价值高于重复调用的代价。
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
		IdempotentOperations: []string{
			"/product.v1.ProductService/ScanForIndex",
			"/product.v1.ProductService/SearchFallback",
			"/product.v1.ProductService/BatchGetProductBrief",
		},
	})
	if err != nil {
		return nil, nil, err
	}
	return &ProductConn{conn: conn}, cleanup, nil
}

// productGateway 通过 gRPC 访问 product-service。
type productGateway struct {
	client productv1.ProductServiceClient
	log    *slog.Logger
}

// NewProductGateway 构造网关。
func NewProductGateway(c *ProductConn, logger *slog.Logger) biz.ProductGateway {
	return &productGateway{
		client: productv1.NewProductServiceClient(c.conn),
		log:    logger.With(slog.String("gateway", "product")),
	}
}

func (g *productGateway) ScanForIndex(ctx context.Context, afterID int64,
	limit int) ([]engine.Document, int64, error) {

	reply, err := g.client.ScanForIndex(ctx, &productv1.ScanForIndexRequest{
		AfterId: afterID,
		Limit:   int32(limit),
	})
	if err != nil {
		return nil, 0, err
	}

	docs := make([]engine.Document, 0, len(reply.GetDocuments()))
	for _, d := range reply.GetDocuments() {
		// 已删除的文档不进索引。调用方通过「文档缺失」感知删除，
		// 比传一个带 deleted 标记的文档再让它自己过滤更简单。
		if d.GetDeleted() {
			continue
		}
		docs = append(docs, engine.Document{
			ProductID:        d.GetProductId(),
			SellerID:         d.GetSellerId(),
			Title:            d.GetTitle(),
			Description:      d.GetDescription(),
			PriceCents:       d.GetPriceCents(),
			Condition:        uint8(d.GetCondition()),
			Status:           uint8(d.GetStatus()),
			CategoryID:       d.GetCategoryId(),
			ParentCategoryID: d.GetParentCategoryId(),
			SchoolID:         d.GetSchoolId(),
			CampusID:         d.GetCampusId(),
			CoverKey:         d.GetCoverKey(),
			HeatScore:        d.GetHeatScore(),
			FavoriteCount:    d.GetFavoriteCount(),
			CreatedAt:        d.GetCreatedAt(),
		})
	}
	return docs, reply.GetNextAfterId(), nil
}

func (g *productGateway) SearchFallback(ctx context.Context,
	q biz.FallbackQuery) (*biz.FallbackResult, error) {

	conditions := make([]productv1.Condition, 0, len(q.Conditions))
	for _, c := range q.Conditions {
		conditions = append(conditions, productv1.Condition(c))
	}

	reply, err := g.client.SearchFallback(ctx, &productv1.SearchFallbackRequest{
		Keyword:       q.Keyword,
		MinPriceCents: q.MinPriceCents,
		MaxPriceCents: q.MaxPriceCents,
		Conditions:    conditions,
		CategoryIds:   q.CategoryIDs,
		CampusIds:     q.CampusIDs,
		OnlyAvailable: q.OnlyAvailable,
		Sort:          q.Sort,
		Cursor:        q.Cursor,
		Limit:         int32(q.Limit),
	})
	if err != nil {
		return nil, err
	}
	return &biz.FallbackResult{
		Items:      briefsFromProto(reply.GetProducts()),
		NextCursor: reply.GetNextCursor(),
		HasMore:    reply.GetHasMore(),
	}, nil
}

func (g *productGateway) BatchGetBrief(ctx context.Context,
	ids []int64) (map[int64]*biz.ProductBrief, error) {

	if len(ids) == 0 {
		return map[int64]*biz.ProductBrief{}, nil
	}
	reply, err := g.client.BatchGetProductBrief(ctx,
		&productv1.BatchGetProductBriefRequest{ProductIds: ids})
	if err != nil {
		return nil, err
	}
	out := make(map[int64]*biz.ProductBrief, len(reply.GetProducts()))
	for id, p := range reply.GetProducts() {
		out[id] = briefFromProto(p)
	}
	return out, nil
}

func briefFromProto(p *productv1.ProductBrief) *biz.ProductBrief {
	if p == nil {
		return nil
	}
	b := &biz.ProductBrief{
		ProductID:     p.GetProductId(),
		SellerID:      p.GetSellerId(),
		Title:         p.GetTitle(),
		PriceCents:    p.GetPriceCents(),
		Condition:     uint8(p.GetCondition()),
		Status:        uint8(p.GetStatus()),
		FavoriteCount: p.GetFavoriteCount(),
		CreatedAt:     p.GetCreatedAt(),
	}
	if cover := p.GetCover(); cover != nil {
		b.CoverKey = cover.GetObjectKey()
	}
	if campus := p.GetCampus(); campus != nil {
		b.CampusID = campus.GetCampusId()
		if school := campus.GetSchool(); school != nil {
			b.SchoolID = school.GetSchoolId()
		}
	}
	return b
}

func briefsFromProto(ps []*productv1.ProductBrief) []*biz.ProductBrief {
	out := make([]*biz.ProductBrief, 0, len(ps))
	for _, p := range ps {
		if b := briefFromProto(p); b != nil {
			out = append(out, b)
		}
	}
	return out
}

// indexSyncState 基于 Redis 的索引同步状态。
//
// 用 Redis 而非数据库：这是一个高频写入（每条事件都更新）、
// 低价值（丢失只影响监控展示）的状态，不值得为它引入一张表和一次磁盘写。
type indexSyncState struct {
	cache     cache.Cacher
	deadStore deadLetterCounter
}

// deadLetterCounter 死信计数来源。
//
// 只声明 Count 而非复用 mq.DeadLetterStore：indexSyncState 只需要计数，
// 依赖完整接口会让它拿到 Requeue/Discard 这些不该拥有的能力。
type deadLetterCounter interface {
	Count(ctx context.Context, topic string) (int64, error)
}

// NewIndexSyncState 构造状态存储。
func NewIndexSyncState(data *Data, dead deadLetterCounter) biz.IndexSyncState {
	return &indexSyncState{cache: data.Cache, deadStore: dead}
}

const lastSyncedKey = "search:last_synced_at"

func (s *indexSyncState) MarkSynced(ctx context.Context, at time.Time) error {
	// TTL 设为 7 天：超过这个时长没有任何同步，说明服务已长期异常，
	// 此时「最近同步时间」这个值本身已无意义，让它过期比留一个陈旧值更清晰。
	return s.cache.Set(ctx, lastSyncedKey,
		[]byte(strconv.FormatInt(at.Unix(), 10)), 7*24*time.Hour)
}

func (s *indexSyncState) LastSynced(ctx context.Context) (time.Time, error) {
	b, err := s.cache.Get(ctx, lastSyncedKey)
	if err != nil {
		// 未命中不是错误：服务刚启动时本就没有同步记录。
		return time.Time{}, nil
	}
	sec, err := strconv.ParseInt(string(b), 10, 64)
	if err != nil {
		return time.Time{}, nil
	}
	return time.Unix(sec, 0), nil
}

func (s *indexSyncState) DeadLetterCount(ctx context.Context) (int64, error) {
	if s.deadStore == nil {
		return 0, nil
	}
	// 统计商品相关主题的死信总数：任一主题积压都意味着索引在丢更新。
	var total int64
	for _, topic := range []string{
		biz.TopicProductCreated,
		biz.TopicProductUpdated,
		biz.TopicProductStatusChanged,
		biz.TopicProductDeleted,
	} {
		n, err := s.deadStore.Count(ctx, topic)
		if err != nil {
			continue
		}
		total += n
	}
	return total, nil
}
