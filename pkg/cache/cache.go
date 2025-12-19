package cache

import (
	"sync"
	"time"
)

// Cache 通用缓存接口
type Cache[K comparable, V any] interface {
	Get(key K) (V, bool)
	Set(key K, value V, ttl time.Duration)
	Delete(key K)
	Clear()
	Size() int
}

// InMemoryCache 内存缓存实现
type InMemoryCache[K comparable, V any] struct {
	items      map[K]*cacheItem[V]
	mu         sync.RWMutex
	defaultTTL time.Duration
}

// cacheItem 缓存项
type cacheItem[V any] struct {
	value      V
	expiresAt  time.Time
}

// NewInMemoryCache 创建新的内存缓存
func NewInMemoryCache[K comparable, V any](defaultTTL time.Duration) *InMemoryCache[K, V] {
	cache := &InMemoryCache[K, V]{
		items:      make(map[K]*cacheItem[V]),
		defaultTTL: defaultTTL,
	}
	
	// 启动清理 goroutine
	go cache.startCleanup()
	
	return cache
}

// Get 获取缓存值
func (c *InMemoryCache[K, V]) Get(key K) (V, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	
	item, exists := c.items[key]
	if !exists {
		var zero V
		return zero, false
	}
	
	// 检查是否过期
	if time.Now().After(item.expiresAt) {
		// 异步删除过期项
		go c.Delete(key)
		var zero V
		return zero, false
	}
	
	return item.value, true
}

// Set 设置缓存值
func (c *InMemoryCache[K, V]) Set(key K, value V, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	
	if ttl == 0 {
		ttl = c.defaultTTL
	}
	
	c.items[key] = &cacheItem[V]{
		value:     value,
		expiresAt: time.Now().Add(ttl),
	}
}

// Delete 删除缓存项
func (c *InMemoryCache[K, V]) Delete(key K) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.items, key)
}

// Clear 清空缓存
func (c *InMemoryCache[K, V]) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items = make(map[K]*cacheItem[V])
}

// Size 获取缓存大小
func (c *InMemoryCache[K, V]) Size() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.items)
}

// startCleanup 启动清理 goroutine（定期清理过期项）
func (c *InMemoryCache[K, V]) startCleanup() {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()
	
	for range ticker.C {
		c.cleanup()
	}
}

// cleanup 清理过期项
func (c *InMemoryCache[K, V]) cleanup() {
	c.mu.Lock()
	defer c.mu.Unlock()
	
	now := time.Now()
	for key, item := range c.items {
		if now.After(item.expiresAt) {
			delete(c.items, key)
		}
	}
}

// PriceCache 价格缓存（专门用于缓存价格数据）
type PriceCache struct {
	cache *InMemoryCache[string, float64]
}

// NewPriceCache 创建新的价格缓存
func NewPriceCache() *PriceCache {
	return &PriceCache{
		cache: NewInMemoryCache[string, float64](5 * time.Minute), // 价格缓存 5 分钟
	}
}

// Get 获取价格
func (pc *PriceCache) Get(assetID string) (float64, bool) {
	return pc.cache.Get(assetID)
}

// Set 设置价格
func (pc *PriceCache) Set(assetID string, price float64) {
	pc.cache.Set(assetID, price, 5*time.Minute)
}

// OrderStatusCache 订单状态缓存（用于缓存订单状态，避免频繁查询）
type OrderStatusCache struct {
	cache *InMemoryCache[string, bool] // orderID -> isOpen
}

// NewOrderStatusCache 创建新的订单状态缓存
func NewOrderStatusCache() *OrderStatusCache {
	return &OrderStatusCache{
		cache: NewInMemoryCache[string, bool](30 * time.Second), // 订单状态缓存 30 秒
	}
}

// Get 获取订单状态（true = open, false = filled/canceled）
func (osc *OrderStatusCache) Get(orderID string) (bool, bool) {
	return osc.cache.Get(orderID)
}

// Set 设置订单状态
func (osc *OrderStatusCache) Set(orderID string, isOpen bool) {
	osc.cache.Set(orderID, isOpen, 30*time.Second)
}

// Delete 删除订单状态
func (osc *OrderStatusCache) Delete(orderID string) {
	osc.cache.Delete(orderID)
}

