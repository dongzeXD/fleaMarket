// Package idgen 提供分布式唯一 ID 生成。
//
// 采用 Snowflake：64 位 = 1 符号位 + 41 位毫秒时间戳 + 10 位节点 + 12 位序列。
// 选择理由：ID 单调递增使其可直接作为 InnoDB 主键（避免 UUID 随机写导致的页分裂），
// 且自带时间信息便于按时间范围粗筛。
//
// 本包不负责节点号的分配来源（环境变量或 etcd 租约），由调用方传入，
// 以保证公共库不依赖任何具体注册中心。
package idgen

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

const (
	// nodeBits 节点位数，支持 1024 个实例。
	nodeBits = 10
	// seqBits 序列位数，单节点单毫秒支持 4096 个 ID。
	seqBits = 12

	maxNode = -1 ^ (-1 << nodeBits)
	maxSeq  = -1 ^ (-1 << seqBits)

	timeShift = nodeBits + seqBits
	nodeShift = seqBits
)

// Epoch 起始时间戳（毫秒），2024-01-01 00:00:00 UTC。
// 41 位毫秒可用约 69 年，即到 2093 年。
const Epoch int64 = 1704067200000

// ErrInvalidNode 节点号越界。
var ErrInvalidNode = errors.New("idgen: node id out of range [0, 1023]")

// Generator Snowflake 生成器，并发安全。
type Generator struct {
	mu   sync.Mutex
	node int64
	last int64
	seq  int64
	// now 可替换的时钟，仅用于测试。
	now func() int64
}

// New 创建生成器。node 取值 0~1023，需保证同一时刻集群内不重复。
func New(node int64) (*Generator, error) {
	if node < 0 || node > maxNode {
		return nil, fmt.Errorf("%w: got %d", ErrInvalidNode, node)
	}
	return &Generator{
		node: node,
		last: -1,
		now:  nowMillis,
	}, nil
}

func nowMillis() int64 { return time.Now().UnixMilli() }

// NextID 生成下一个 ID。
//
// 时钟回拨处理：不 panic、不返回错误，而是自旋等待到上次的时间点。
// 理由——ID 生成位于所有写请求的关键路径上，返回错误会让回拨期间整个服务写入不可用；
// 回拨通常是毫秒级 NTP 校正，自旋等待的代价远小于服务不可用。
// 若回拨超过 ToleranceMillis 则说明是异常的大幅回拨，此时返回错误交由上层告警。
func (g *Generator) NextID() (int64, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	ts := g.now()
	if ts < g.last {
		if g.last-ts > ToleranceMillis {
			return 0, fmt.Errorf("idgen: clock moved backwards by %dms, exceeds tolerance %dms",
				g.last-ts, ToleranceMillis)
		}
		for ts < g.last {
			time.Sleep(time.Millisecond)
			ts = g.now()
		}
	}

	if ts == g.last {
		g.seq = (g.seq + 1) & maxSeq
		if g.seq == 0 {
			// 当前毫秒序列耗尽，等到下一毫秒。
			for ts <= g.last {
				ts = g.now()
			}
		}
	} else {
		g.seq = 0
	}
	g.last = ts

	return (ts-Epoch)<<timeShift | g.node<<nodeShift | g.seq, nil
}

// ToleranceMillis 可容忍的时钟回拨上限。
const ToleranceMillis int64 = 5000

// MustNextID 生成 ID，出错时 panic。
// 仅用于初始化阶段等不便处理错误的场景，业务路径请用 NextID。
func (g *Generator) MustNextID() int64 {
	id, err := g.NextID()
	if err != nil {
		panic(err)
	}
	return id
}

// TimeOf 从 ID 中还原生成时间。便于排查问题时定位数据产生的时刻。
func TimeOf(id int64) time.Time {
	return time.UnixMilli(id>>timeShift + Epoch)
}

// NodeOf 从 ID 中还原生成节点号。
func NodeOf(id int64) int64 {
	return (id >> nodeShift) & maxNode
}
