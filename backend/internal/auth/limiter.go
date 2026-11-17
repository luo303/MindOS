package auth

import (
	"sync"
	"time"
)

// Limiter 是进程内的固定窗口限流器，按 key（客户端 IP）计数。
//
// 为什么不做成分布式：本项目是单进程部署，登录本来就要查库（会话），
// 多一个 Redis 只为一个计数器不值。多实例时每个实例各自计数，
// 属于"降低噪声"而不是硬防线——真正的账号锁定靠 users.failed_logins，
// 那份状态在数据库里，跨实例有效。
type Limiter struct {
	mu      sync.Mutex
	limit   int
	window  time.Duration
	buckets map[string]bucket
	// lastSweep 控制清理频率：没人来的时候不该一直扫 map。
	lastSweep time.Time
}

type bucket struct {
	start time.Time
	count int
}

// NewLimiter 创建限流器。limit <= 0 时恒放行（测试与"关掉限流"用）。
func NewLimiter(limit int, window time.Duration) *Limiter {
	return &Limiter{
		limit:   limit,
		window:  window,
		buckets: make(map[string]bucket),
	}
}

// Allow 记一次访问并返回是否放行。
func (l *Limiter) Allow(key string) bool {
	if l.limit <= 0 {
		return true
	}
	now := time.Now()

	l.mu.Lock()
	defer l.mu.Unlock()

	// 每分钟顺手清一次过期桶。清理是 O(窗口内活跃 IP 数)，很小；
	// 不做的话，长期运行会把每个扫过的 IP 永久留在内存里。
	if now.Sub(l.lastSweep) > time.Minute {
		for k, b := range l.buckets {
			if now.Sub(b.start) >= l.window {
				delete(l.buckets, k)
			}
		}
		l.lastSweep = now
	}

	b, ok := l.buckets[key]
	if !ok || now.Sub(b.start) >= l.window {
		l.buckets[key] = bucket{start: now, count: 1}
		return true
	}
	if b.count >= l.limit {
		return false
	}
	b.count++
	l.buckets[key] = b
	return true
}
