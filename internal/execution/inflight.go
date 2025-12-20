package execution

import (
	"fmt"
	"hash/fnv"
	"sync"
	"time"
)

// ErrDuplicateInFlight 表示同一 key 的请求仍在 in-flight（或在 TTL 窗口内）。
// 用于防止高频触发导致的重复下单/重复 IO。
var ErrDuplicateInFlight = fmt.Errorf("duplicate in-flight")

// InFlightDeduper 提供“短时间窗口内的确定性去重”。
//
// 设计目标：
// - 不允许误判（避免 bitset 哈希冲突导致的误跳过下单）
// - 开销可控（分片 map，短 TTL，清理惰性进行）
//
// 注意：这是“工程化去重”，不是严格意义的无锁原子位图去重；
// 交易系统里误判的代价高，因此优先选择确定性。
type InFlightDeduper struct {
	ttl    time.Duration
	shards []inFlightShard
}

type inFlightShard struct {
	mu sync.Mutex
	m  map[string]time.Time // key -> expiresAt
}

// NewInFlightDeduper 创建去重器。
// ttl 建议取 500ms~5s（覆盖一次信号处理到下单完成的典型窗口）。
func NewInFlightDeduper(ttl time.Duration, shardCount int) *InFlightDeduper {
	if ttl <= 0 {
		ttl = 2 * time.Second
	}
	if shardCount <= 0 {
		shardCount = 64
	}
	shards := make([]inFlightShard, shardCount)
	for i := range shards {
		shards[i].m = make(map[string]time.Time)
	}
	return &InFlightDeduper{ttl: ttl, shards: shards}
}

// TryAcquire 尝试获取 key 的 in-flight 令牌。
// - 成功返回 nil
// - 失败返回 ErrDuplicateInFlight
func (d *InFlightDeduper) TryAcquire(key string) error {
	if d == nil {
		return nil
	}
	if key == "" {
		return nil
	}
	now := time.Now()
	sh := d.shard(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()

	// 惰性清理：仅清理本 shard 中过期项，且仅在发生访问时进行
	for k, exp := range sh.m {
		if !exp.After(now) {
			delete(sh.m, k)
		}
	}

	if exp, ok := sh.m[key]; ok && exp.After(now) {
		return ErrDuplicateInFlight
	}
	sh.m[key] = now.Add(d.ttl)
	return nil
}

// Release 提前释放 key（允许更快再次进入）。
func (d *InFlightDeduper) Release(key string) {
	if d == nil || key == "" {
		return
	}
	sh := d.shard(key)
	sh.mu.Lock()
	delete(sh.m, key)
	sh.mu.Unlock()
}

func (d *InFlightDeduper) shard(key string) *inFlightShard {
	h := fnv.New32a()
	_, _ = h.Write([]byte(key))
	idx := int(h.Sum32()) % len(d.shards)
	return &d.shards[idx]
}

