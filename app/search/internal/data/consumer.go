package data

import (
	"context"
	"log/slog"

	"github.com/fleamarket/app/search/internal/biz"
	"github.com/fleamarket/app/search/internal/conf"
	"github.com/fleamarket/app/search/internal/engine"

	"github.com/fleamarket/pkg/mq"
)

// Consumer 消费 product.* 事件并维护索引。
//
// 作为 kratos 的 transport.Server 挂载到 app：
// 这样它的生命周期由框架管理，优雅停机时会先停止拉取新消息。
type Consumer struct {
	consumer mq.Consumer
	uc       *biz.SearchUsecase
	eng      engine.Engine
	log      *slog.Logger
	// stop 用于 Stop 时取消 Start 中的 ctx。
	stop context.CancelFunc
}

// NewConsumer 构造消费者并注册各主题的处理函数。
func NewConsumer(data *Data, c *conf.Business, uc *biz.SearchUsecase,
	eng engine.Engine, logger *slog.Logger) (*Consumer, error) {

	log := logger.With(slog.String("component", "index-consumer"))

	mc, err := mq.NewRedisStreamConsumer(mq.RedisStreamConfig{
		Client:    data.Cache.Client(),
		Namespace: c.GetMqNamespace(),
		// 消费组取服务名：同组内一条消息只被一个实例处理，
		// 多实例部署时索引不会被重复写入（虽然 Upsert 幂等，
		// 但重复写会浪费索引吞吐）。
		Group:        "search-service",
		ConsumerName: c.GetInstanceId(),
		MaxRetries:   c.GetMqMaxRetries(),
		Logger:       log,
	})
	if err != nil {
		return nil, err
	}

	handler := func(ctx context.Context, msg *mq.Message) error {
		return uc.HandleProductEvent(ctx, msg.Topic, msg.Payload)
	}

	for _, topic := range []string{
		biz.TopicProductCreated,
		biz.TopicProductUpdated,
		biz.TopicProductStatusChanged,
		biz.TopicProductDeleted,
	} {
		if err := mc.Subscribe(topic, handler); err != nil {
			return nil, err
		}
	}

	return &Consumer{consumer: mc, uc: uc, eng: eng, log: log}, nil
}

// Start 实现 kratos 的 transport.Server。
//
// 启动时先确保索引与字段配置就位：索引配置是代码的一部分，
// 靠人工在控制台配置会让新环境的部署遗漏配置而行为不一致。
func (c *Consumer) Start(ctx context.Context) error {
	if err := c.eng.EnsureIndex(ctx); err != nil {
		// 索引创建失败不阻止服务启动：检索会走降级路径，
		// 消费者也会持续重试。让服务起不来反而使降级能力无从发挥。
		c.log.ErrorContext(ctx, "索引初始化失败，检索将走降级路径",
			slog.Any("error", err))
	}

	runCtx, cancel := context.WithCancel(context.Background())
	c.stop = cancel

	go func() {
		if err := c.consumer.Start(runCtx); err != nil && runCtx.Err() == nil {
			c.log.Error("事件消费退出", slog.Any("error", err))
		}
	}()
	return nil
}

// Stop 停止消费。
func (c *Consumer) Stop(context.Context) error {
	if c.stop != nil {
		c.stop()
	}
	return c.consumer.Close()
}

// NewDeadLetterCounter 构造死信计数器。
//
// 不从 Consumer 取，而是独立创建：Consumer 依赖 SearchUsecase，
// SearchUsecase 依赖 IndexSyncState，IndexSyncState 又要读死信数——
// 从 Consumer 取会形成依赖环。查死信本身不需要消费组，
// 因此 pkg/mq 提供了独立的 RedisStreamDeadLetters。
func NewDeadLetterCounter(data *Data, c *conf.Business) (deadLetterCounter, error) {
	return mq.NewRedisStreamDeadLetters(mq.RedisStreamConfig{
		Client:    data.Cache.Client(),
		Namespace: c.GetMqNamespace(),
	})
}
