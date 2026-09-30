package mq

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	fieldTopic     = "topic"
	fieldPayload   = "payload"
	fieldHeaderPfx = "h:"
)

// RedisStreamConfig Redis Stream 实现的配置。
type RedisStreamConfig struct {
	// Client Redis 客户端。由调用方传入以复用同一连接池。
	Client *redis.Client
	// Namespace stream key 前缀，用于多环境隔离，如 "fleamarket:prod"。
	Namespace string
	// Group 消费组名，通常取服务名。同组内消息只被一个实例消费。
	Group string
	// ConsumerName 消费者名，通常取实例 ID。
	ConsumerName string
	// MaxRetries 最大投递次数，超过则转入死信。默认 3。
	MaxRetries int64
	// BlockTimeout 无消息时的阻塞等待时长，默认 5s。
	BlockTimeout time.Duration
	// BatchSize 单次拉取条数，默认 16。
	BatchSize int64
	// MaxLen stream 最大长度，超出则裁剪最旧消息。默认 100000。
	//
	// 必须设置上限：Redis Stream 默认无界，若消费端长期落后会把内存吃光。
	// 裁剪是有损的，但对通知这类非关键消息而言，比 Redis OOM 更可接受。
	MaxLen int64
	// ClaimMinIdle 认领他人未确认消息的空闲阈值，默认 30s。
	//
	// 用于处理消费者崩溃：消息已投递但未确认，若不认领会永远留在
	// pending 列表中无人处理。
	ClaimMinIdle time.Duration
	// Logger 日志器。
	Logger *slog.Logger
}

func (c *RedisStreamConfig) setDefaults() error {
	if c.Client == nil {
		return errors.New("mq: nil redis client")
	}
	if c.Group == "" {
		return errors.New("mq: empty consumer group")
	}
	if c.ConsumerName == "" {
		return errors.New("mq: empty consumer name")
	}
	if c.MaxRetries <= 0 {
		c.MaxRetries = 3
	}
	if c.BlockTimeout <= 0 {
		c.BlockTimeout = 5 * time.Second
	}
	if c.BatchSize <= 0 {
		c.BatchSize = 16
	}
	if c.MaxLen <= 0 {
		c.MaxLen = 100000
	}
	if c.ClaimMinIdle <= 0 {
		c.ClaimMinIdle = 30 * time.Second
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	return nil
}

func streamKey(ns, topic string) string {
	if ns == "" {
		return "mq:" + topic
	}
	return ns + ":mq:" + topic
}

func deadKey(ns, topic string) string {
	return streamKey(ns, topic) + ":dead"
}

// RedisStreamProducer 基于 Redis Stream 的生产者。
type RedisStreamProducer struct {
	rdb    *redis.Client
	ns     string
	maxLen int64
}

var _ Producer = (*RedisStreamProducer)(nil)

// NewRedisStreamProducer 创建生产者。
func NewRedisStreamProducer(c RedisStreamConfig) (*RedisStreamProducer, error) {
	if c.Client == nil {
		return nil, errors.New("mq: nil redis client")
	}
	if c.MaxLen <= 0 {
		c.MaxLen = 100000
	}
	return &RedisStreamProducer{rdb: c.Client, ns: c.Namespace, maxLen: c.MaxLen}, nil
}

func (p *RedisStreamProducer) Publish(ctx context.Context, topic string,
	payload []byte, headers map[string]string) (string, error) {

	if topic == "" {
		return "", errors.New("mq: empty topic")
	}
	values := map[string]any{
		fieldTopic:   topic,
		fieldPayload: payload,
	}
	for k, v := range headers {
		values[fieldHeaderPfx+k] = v
	}
	id, err := p.rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: streamKey(p.ns, topic),
		// Approx 让 Redis 按宏观节点裁剪，代价是长度不精确，
		// 换来的是 O(1) 而非 O(N) 的裁剪开销。
		MaxLen: p.maxLen,
		Approx: true,
		Values: values,
	}).Result()
	if err != nil {
		return "", fmt.Errorf("mq: publish to %s: %w", topic, err)
	}
	return id, nil
}

func (p *RedisStreamProducer) Close() error { return nil }

// RedisStreamDeadLetters 独立的死信存取实现。
//
// 单独提供而非只挂在 Consumer 上：查询死信不需要消费组，
// 而把它绑定在 Consumer 上会造成依赖环——
// 索引状态要读死信数，消费者要用索引用例，用例又要索引状态。
// 管理后台也需要在不启动消费者的进程里查死信。
type RedisStreamDeadLetters struct {
	rdb    *redis.Client
	ns     string
	maxLen int64
}

var _ DeadLetterStore = (*RedisStreamDeadLetters)(nil)

// NewRedisStreamDeadLetters 创建死信存取器。
func NewRedisStreamDeadLetters(c RedisStreamConfig) (*RedisStreamDeadLetters, error) {
	if c.Client == nil {
		return nil, errors.New("mq: nil redis client")
	}
	if c.MaxLen <= 0 {
		c.MaxLen = 100000
	}
	return &RedisStreamDeadLetters{rdb: c.Client, ns: c.Namespace, maxLen: c.MaxLen}, nil
}

func (d *RedisStreamDeadLetters) List(ctx context.Context, topic string, limit int64) ([]*Message, error) {
	if limit <= 0 {
		limit = 50
	}
	res, err := d.rdb.XRangeN(ctx, deadKey(d.ns, topic), "-", "+", limit).Result()
	if err != nil {
		return nil, err
	}
	out := make([]*Message, 0, len(res))
	for i := range res {
		out = append(out, toMessage(topic, &res[i]))
	}
	return out, nil
}

func (d *RedisStreamDeadLetters) Requeue(ctx context.Context, topic string, ids ...string) (int, error) {
	dk := deadKey(d.ns, topic)
	sk := streamKey(d.ns, topic)

	n := 0
	for _, id := range ids {
		res, err := d.rdb.XRangeN(ctx, dk, id, id, 1).Result()
		if err != nil {
			return n, err
		}
		if len(res) == 0 {
			continue
		}
		values := make(map[string]any, len(res[0].Values))
		for k, v := range res[0].Values {
			// 重投时剥掉死信元数据，避免污染业务字段。
			if k == "dead_at" || k == "origin_id" {
				continue
			}
			values[k] = v
		}
		if err := d.rdb.XAdd(ctx, &redis.XAddArgs{
			Stream: sk, MaxLen: d.maxLen, Approx: true, Values: values,
		}).Err(); err != nil {
			return n, err
		}
		if err := d.rdb.XDel(ctx, dk, id).Err(); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

func (d *RedisStreamDeadLetters) Discard(ctx context.Context, topic string, ids ...string) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	n, err := d.rdb.XDel(ctx, deadKey(d.ns, topic), ids...).Result()
	return int(n), err
}

func (d *RedisStreamDeadLetters) Count(ctx context.Context, topic string) (int64, error) {
	n, err := d.rdb.XLen(ctx, deadKey(d.ns, topic)).Result()
	if errors.Is(err, redis.Nil) {
		return 0, nil
	}
	return n, err
}

// RedisStreamConsumer 基于 Redis Stream 消费组的消费者。
type RedisStreamConsumer struct {
	cfg      RedisStreamConfig
	handlers map[string]Handler
	mu       sync.RWMutex
	started  bool
	closed   bool
}

var (
	_ Consumer        = (*RedisStreamConsumer)(nil)
	_ DeadLetterStore = (*RedisStreamConsumer)(nil)
)

// NewRedisStreamConsumer 创建消费者。
func NewRedisStreamConsumer(c RedisStreamConfig) (*RedisStreamConsumer, error) {
	if err := c.setDefaults(); err != nil {
		return nil, err
	}
	return &RedisStreamConsumer{cfg: c, handlers: make(map[string]Handler)}, nil
}

func (c *RedisStreamConsumer) Subscribe(topic string, h Handler) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.started {
		return errors.New("mq: cannot subscribe after Start")
	}
	if topic == "" || h == nil {
		return errors.New("mq: empty topic or nil handler")
	}
	if _, dup := c.handlers[topic]; dup {
		return fmt.Errorf("mq: topic %q already subscribed", topic)
	}
	c.handlers[topic] = h
	return nil
}

// Start 为每个订阅的主题启动一个消费协程，阻塞至 ctx 取消。
func (c *RedisStreamConsumer) Start(ctx context.Context) error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return ErrClosed
	}
	if len(c.handlers) == 0 {
		c.mu.Unlock()
		return errors.New("mq: no topic subscribed")
	}
	c.started = true
	topics := make([]string, 0, len(c.handlers))
	for t := range c.handlers {
		topics = append(topics, t)
	}
	c.mu.Unlock()

	// 消费组必须先创建。MKSTREAM 使 stream 不存在时一并创建，
	// 避免消费者启动早于第一条消息时报错。
	for _, t := range topics {
		key := streamKey(c.cfg.Namespace, t)
		if err := c.cfg.Client.XGroupCreateMkStream(ctx, key, c.cfg.Group, "0").Err(); err != nil &&
			!isBusyGroup(err) {
			return fmt.Errorf("mq: create group for %s: %w", t, err)
		}
	}

	var wg sync.WaitGroup
	for _, t := range topics {
		wg.Add(1)
		go func(topic string) {
			defer wg.Done()
			c.consumeLoop(ctx, topic)
		}(t)
	}
	wg.Wait()
	return ctx.Err()
}

func (c *RedisStreamConsumer) consumeLoop(ctx context.Context, topic string) {
	key := streamKey(c.cfg.Namespace, topic)
	log := c.cfg.Logger.With(slog.String("mq.topic", topic))

	// 认领定时器：周期性接手崩溃实例遗留的未确认消息。
	claimTicker := time.NewTicker(c.cfg.ClaimMinIdle)
	defer claimTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-claimTicker.C:
			c.claimStale(ctx, topic, key, log)
		default:
		}

		streams, err := c.cfg.Client.XReadGroup(ctx, &redis.XReadGroupArgs{
			Group:    c.cfg.Group,
			Consumer: c.cfg.ConsumerName,
			Streams:  []string{key, ">"},
			Count:    c.cfg.BatchSize,
			Block:    c.cfg.BlockTimeout,
		}).Result()

		if err != nil {
			if errors.Is(err, redis.Nil) || errors.Is(err, context.Canceled) {
				continue
			}
			if ctx.Err() != nil {
				return
			}
			log.Error("mq: read group failed", slog.Any("error", err))
			// 退避后重试，避免 Redis 故障时打满日志与 CPU。
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
			continue
		}

		for _, s := range streams {
			for i := range s.Messages {
				c.handleOne(ctx, topic, key, &s.Messages[i], log)
			}
		}
	}
}

func (c *RedisStreamConsumer) handleOne(ctx context.Context, topic, key string,
	raw *redis.XMessage, log *slog.Logger) {

	msg := toMessage(topic, raw)

	c.mu.RLock()
	h := c.handlers[topic]
	c.mu.RUnlock()
	if h == nil {
		return
	}

	// 取投递次数：XPENDING 的 delivery count 才是权威值。
	// 这里用 XAutoClaim/XPending 成本较高，改为在处理失败时查询一次。
	err := h(ctx, msg)
	if err == nil {
		if ackErr := c.cfg.Client.XAck(ctx, key, c.cfg.Group, raw.ID).Err(); ackErr != nil {
			log.Error("mq: ack failed", slog.String("mq.id", raw.ID), slog.Any("error", ackErr))
		}
		return
	}

	delivered := c.deliveryCount(ctx, key, raw.ID)
	log.Warn("mq: handler failed",
		slog.String("mq.id", raw.ID),
		slog.Int64("mq.delivered", delivered),
		slog.Any("error", err))

	if delivered >= c.cfg.MaxRetries {
		if dErr := c.toDeadLetter(ctx, topic, key, raw); dErr != nil {
			log.Error("mq: move to dead letter failed",
				slog.String("mq.id", raw.ID), slog.Any("error", dErr))
			return
		}
		log.Error("mq: message moved to dead letter",
			slog.String("mq.id", raw.ID), slog.Int64("mq.delivered", delivered))
	}
	// 未达上限则不 ACK，消息留在 pending，由后续认领重新投递。
}

func (c *RedisStreamConsumer) deliveryCount(ctx context.Context, key, id string) int64 {
	res, err := c.cfg.Client.XPendingExt(ctx, &redis.XPendingExtArgs{
		Stream: key,
		Group:  c.cfg.Group,
		Start:  id,
		End:    id,
		Count:  1,
	}).Result()
	if err != nil || len(res) == 0 {
		return 1
	}
	return res[0].RetryCount
}

// toDeadLetter 把消息搬到死信 stream 并确认原消息。
//
// 先写死信、后 ACK：顺序反了会在两步之间崩溃时丢消息。
// 重复写死信（已写入但未 ACK 时崩溃）比丢消息可接受得多。
func (c *RedisStreamConsumer) toDeadLetter(ctx context.Context, topic, key string,
	raw *redis.XMessage) error {

	values := make(map[string]any, len(raw.Values)+2)
	for k, v := range raw.Values {
		values[k] = v
	}
	values["dead_at"] = strconv.FormatInt(time.Now().Unix(), 10)
	values["origin_id"] = raw.ID

	if err := c.cfg.Client.XAdd(ctx, &redis.XAddArgs{
		Stream: deadKey(c.cfg.Namespace, topic),
		Values: values,
	}).Err(); err != nil {
		return err
	}
	return c.cfg.Client.XAck(ctx, key, c.cfg.Group, raw.ID).Err()
}

// claimStale 认领空闲过久的未确认消息，处理消费者崩溃遗留的 pending。
func (c *RedisStreamConsumer) claimStale(ctx context.Context, topic, key string, log *slog.Logger) {
	msgs, _, err := c.cfg.Client.XAutoClaim(ctx, &redis.XAutoClaimArgs{
		Stream:   key,
		Group:    c.cfg.Group,
		Consumer: c.cfg.ConsumerName,
		MinIdle:  c.cfg.ClaimMinIdle,
		Start:    "0",
		Count:    c.cfg.BatchSize,
	}).Result()
	if err != nil {
		if ctx.Err() == nil {
			log.Warn("mq: auto claim failed", slog.Any("error", err))
		}
		return
	}
	for i := range msgs {
		c.handleOne(ctx, topic, key, &msgs[i], log)
	}
}

func (c *RedisStreamConsumer) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	return nil
}

// --- DeadLetterStore ---

func (c *RedisStreamConsumer) List(ctx context.Context, topic string, limit int64) ([]*Message, error) {
	if limit <= 0 {
		limit = 50
	}
	res, err := c.cfg.Client.XRangeN(ctx, deadKey(c.cfg.Namespace, topic), "-", "+", limit).Result()
	if err != nil {
		return nil, err
	}
	out := make([]*Message, 0, len(res))
	for i := range res {
		out = append(out, toMessage(topic, &res[i]))
	}
	return out, nil
}

func (c *RedisStreamConsumer) Requeue(ctx context.Context, topic string, ids ...string) (int, error) {
	dk := deadKey(c.cfg.Namespace, topic)
	sk := streamKey(c.cfg.Namespace, topic)

	n := 0
	for _, id := range ids {
		res, err := c.cfg.Client.XRangeN(ctx, dk, id, id, 1).Result()
		if err != nil {
			return n, err
		}
		if len(res) == 0 {
			continue
		}
		values := make(map[string]any, len(res[0].Values))
		for k, v := range res[0].Values {
			// 重投时剥掉死信元数据，避免污染业务字段。
			if k == "dead_at" || k == "origin_id" {
				continue
			}
			values[k] = v
		}
		if err := c.cfg.Client.XAdd(ctx, &redis.XAddArgs{
			Stream: sk,
			MaxLen: c.cfg.MaxLen,
			Approx: true,
			Values: values,
		}).Err(); err != nil {
			return n, err
		}
		if err := c.cfg.Client.XDel(ctx, dk, id).Err(); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

func (c *RedisStreamConsumer) Discard(ctx context.Context, topic string, ids ...string) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	n, err := c.cfg.Client.XDel(ctx, deadKey(c.cfg.Namespace, topic), ids...).Result()
	return int(n), err
}

func (c *RedisStreamConsumer) Count(ctx context.Context, topic string) (int64, error) {
	n, err := c.cfg.Client.XLen(ctx, deadKey(c.cfg.Namespace, topic)).Result()
	if errors.Is(err, redis.Nil) {
		return 0, nil
	}
	return n, err
}

func toMessage(topic string, raw *redis.XMessage) *Message {
	m := &Message{ID: raw.ID, Topic: topic, Headers: map[string]string{}}
	for k, v := range raw.Values {
		s, _ := v.(string)
		switch {
		case k == fieldTopic:
			if s != "" {
				m.Topic = s
			}
		case k == fieldPayload:
			m.Payload = []byte(s)
		case len(k) > len(fieldHeaderPfx) && k[:len(fieldHeaderPfx)] == fieldHeaderPfx:
			m.Headers[k[len(fieldHeaderPfx):]] = s
		}
	}
	// Redis Stream ID 形如 "1700000000000-0"，前半段即毫秒时间戳。
	if ts := parseStreamTime(raw.ID); !ts.IsZero() {
		m.Timestamp = ts
	}
	return m
}

func parseStreamTime(id string) time.Time {
	for i := 0; i < len(id); i++ {
		if id[i] == '-' {
			ms, err := strconv.ParseInt(id[:i], 10, 64)
			if err != nil {
				return time.Time{}
			}
			return time.UnixMilli(ms)
		}
	}
	return time.Time{}
}

func isBusyGroup(err error) bool {
	return err != nil && err.Error() == "BUSYGROUP Consumer Group name already exists"
}
