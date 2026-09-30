// Package mq 提供消息队列抽象与 Redis Stream 实现。
//
// 为什么先用 Redis Stream 而不是 Kafka：本平台的异步场景（通知扇出、
// 搜索索引同步、内容审核回写）量级有限，而 Redis 已是必需组件。
// 多引入一个 Kafka 集群会显著增加运维面而收益不明显。
//
// 但接口与实现严格分离，使日后换 Kafka 只需新增一个实现——
// 业务代码只依赖 Producer / Consumer 接口，不出现任何 Redis 概念。
package mq

import (
	"context"
	"errors"
	"time"
)

// ErrClosed 生产者或消费者已关闭。
var ErrClosed = errors.New("mq: closed")

// Message 一条消息。
type Message struct {
	// ID 消息在队列中的唯一标识，由实现分配。
	ID string
	// Topic 主题，如 "product.created"。
	Topic string
	// Payload 消息体，序列化格式由业务决定。
	Payload []byte
	// Headers 附加元数据，用于透传 traceID 等。
	Headers map[string]string
	// Timestamp 入队时间。
	Timestamp time.Time
	// Delivered 该消息已被投递的次数，用于判断是否应转入死信。
	Delivered int64
}

// Producer 消息生产者。
type Producer interface {
	// Publish 发布消息。返回分配的消息 ID。
	Publish(ctx context.Context, topic string, payload []byte, headers map[string]string) (string, error)
	// Close 关闭生产者。
	Close() error
}

// Handler 消息处理函数。
//
// 返回 nil 表示处理成功，消息将被确认；返回错误则消息保留，
// 在重试上限内会被重新投递，超过上限后转入死信。
//
// 实现必须幂等：任何消息队列都只能保证「至少一次」投递，
// 消费者崩溃在「处理完成」与「确认」之间时，消息必然被重复投递。
type Handler func(ctx context.Context, msg *Message) error

// Consumer 消息消费者。
type Consumer interface {
	// Subscribe 注册主题的处理函数。必须在 Start 之前调用。
	Subscribe(topic string, h Handler) error
	// Start 开始消费，阻塞直到 ctx 取消。
	Start(ctx context.Context) error
	// Close 关闭消费者。
	Close() error
}

// DeadLetterStore 死信存取接口。
//
// 单独抽象而非内建在 Consumer 中，是为了让管理后台能在不依赖消费者实例的
// 前提下查询与重投死信（doc.md §19.3）。
type DeadLetterStore interface {
	// List 分页列出死信。
	List(ctx context.Context, topic string, limit int64) ([]*Message, error)
	// Requeue 把死信重新投回原主题，并从死信队列移除。
	Requeue(ctx context.Context, topic string, ids ...string) (int, error)
	// Discard 丢弃死信。
	Discard(ctx context.Context, topic string, ids ...string) (int, error)
	// Count 死信数量，供监控告警。
	Count(ctx context.Context, topic string) (int64, error)
}
