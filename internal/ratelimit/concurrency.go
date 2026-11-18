package ratelimit

import (
	"maps"
	"sync"
)

// Concurrency 只记录进程内尚未完成的业务，独立于配置快照。
type Concurrency struct {
	mu              sync.Mutex
	global          int64
	accessKeys      map[uint]int64
	groups          map[uint]int64
	accessKeyFences map[uint]int64
}

type ConcurrencySnapshot struct {
	Global     int64
	AccessKeys map[uint]int64
	Groups     map[uint]int64
}

func NewConcurrency() *Concurrency {
	return &Concurrency{accessKeys: make(map[uint]int64), groups: make(map[uint]int64)}
}

// BeginAccessKeyFence 原子暂停指定密钥的新请求，不影响已有请求释放，也不修改计数。
// 空批次、零 ID 或重复 ID 整批拒绝，返回 nil, false。成功时即使密钥忙也可建立保护；
// 调用方应再等待 AccessKeyCurrent 归零。每次调用独立持有引用，release 同步且幂等，
// 只有该密钥的所有持有者释放后才恢复准入。保护仅进程内生效，不修改密钥状态。
func (c *Concurrency) BeginAccessKeyFence(ids []uint) (release func(), ok bool) {
	if len(ids) == 0 {
		return nil, false
	}
	owned := make([]uint, 0, len(ids))
	seen := make(map[uint]struct{}, len(ids))
	for _, id := range ids {
		if id == 0 {
			return nil, false
		}
		if _, exists := seen[id]; exists {
			return nil, false
		}
		seen[id] = struct{}{}
		owned = append(owned, id)
	}

	c.mu.Lock()
	if c.accessKeyFences == nil {
		c.accessKeyFences = make(map[uint]int64)
	}
	for _, id := range owned {
		c.accessKeyFences[id]++
	}
	c.mu.Unlock()
	return sync.OnceFunc(func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		for _, id := range owned {
			c.accessKeyFences[id]--
			if c.accessKeyFences[id] == 0 {
				delete(c.accessKeyFences, id)
			}
		}
	}), true
}

// TryAcquireRequest 同时取得全局和访问密钥名额；调用方在业务收尾后释放。
func (c *Concurrency) TryAcquireRequest(key uint, globalLimit, keyLimit int64) (func(), bool) {
	c.mu.Lock()
	if c.accessKeyFences[key] > 0 || (globalLimit > 0 && c.global >= globalLimit) || (keyLimit > 0 && c.accessKeys[key] >= keyLimit) {
		c.mu.Unlock()
		return nil, false
	}
	if c.accessKeys == nil {
		c.accessKeys = make(map[uint]int64)
	}
	c.global++
	c.accessKeys[key]++
	c.mu.Unlock()
	return sync.OnceFunc(func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.global--
		c.accessKeys[key]--
		if c.accessKeys[key] == 0 {
			delete(c.accessKeys, key)
		}
	}), true
}

func (c *Concurrency) TryAcquireGroup(group uint, limit int64) (func(), bool) {
	c.mu.Lock()
	if limit > 0 && c.groups[group] >= limit {
		c.mu.Unlock()
		return nil, false
	}
	if c.groups == nil {
		c.groups = make(map[uint]int64)
	}
	c.groups[group]++
	c.mu.Unlock()
	return sync.OnceFunc(func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.groups[group]--
		if c.groups[group] == 0 {
			delete(c.groups, group)
		}
	}), true
}

func (c *Concurrency) Snapshot() ConcurrencySnapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	return ConcurrencySnapshot{Global: c.global, AccessKeys: maps.Clone(c.accessKeys), Groups: maps.Clone(c.groups)}
}

func (c *Concurrency) AccessKeyCurrent(id uint) int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.accessKeys[id]
}
