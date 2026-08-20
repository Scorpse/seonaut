package api

import (
	"strconv"
	"sync"
	"time"
)

type RateClass string

const (
	RateNone   RateClass = ""
	RateRead   RateClass = "read"
	RateCrawl  RateClass = "crawl"
	RateExport RateClass = "export"
)

type RateDecision struct {
	Allowed   bool
	Limit     int
	Remaining int
	ResetAt   time.Time
}

type RequestLimiter interface {
	Allow(string, RateClass) RateDecision
}

type rateWindow struct {
	start time.Time
	count int
}

type FixedWindowLimiter struct {
	mu      sync.Mutex
	window  time.Duration
	limits  map[RateClass]int
	windows map[string]rateWindow
	now     func() time.Time
}

func NewFixedWindowLimiter(window time.Duration, limits map[RateClass]int, now func() time.Time) *FixedWindowLimiter {
	if window <= 0 {
		window = time.Minute
	}
	if now == nil {
		now = time.Now
	}
	return &FixedWindowLimiter{window: window, limits: limits, windows: map[string]rateWindow{}, now: now}
}

func (l *FixedWindowLimiter) Allow(key string, class RateClass) RateDecision {
	if l == nil || key == "" || class == RateNone {
		return RateDecision{Allowed: true}
	}
	limit := l.limits[class]
	if limit <= 0 {
		return RateDecision{Allowed: true}
	}
	now := l.now().UTC()
	windowNumber := now.UnixNano() / l.window.Nanoseconds()
	start := time.Unix(0, windowNumber*l.window.Nanoseconds()).UTC()
	mapKey := string(class) + "\x00" + key
	l.mu.Lock()
	defer l.mu.Unlock()
	current := l.windows[mapKey]
	if !current.start.Equal(start) {
		current = rateWindow{start: start}
	}
	allowed := current.count < limit
	if allowed {
		current.count++
	}
	l.windows[mapKey] = current
	remaining := limit - current.count
	if remaining < 0 {
		remaining = 0
	}
	return RateDecision{Allowed: allowed, Limit: limit, Remaining: remaining, ResetAt: start.Add(l.window)}
}

func setRateHeaders(headers interface{ Set(string, string) }, decision RateDecision, now time.Time) {
	if decision.Limit <= 0 {
		return
	}
	reset := int64(time.Until(decision.ResetAt).Seconds())
	if !now.IsZero() {
		reset = int64(decision.ResetAt.Sub(now).Seconds())
	}
	if reset < 1 {
		reset = 1
	}
	headers.Set("RateLimit-Limit", strconv.Itoa(decision.Limit))
	headers.Set("RateLimit-Remaining", strconv.Itoa(decision.Remaining))
	headers.Set("RateLimit-Reset", strconv.FormatInt(reset, 10))
	if !decision.Allowed {
		headers.Set("Retry-After", strconv.FormatInt(reset, 10))
	}
}

type ConcurrencyBudget struct {
	mu         sync.Mutex
	perKey     map[string]int
	total      int
	keyLimit   int
	totalLimit int
}

func NewConcurrencyBudget(keyLimit, totalLimit int) *ConcurrencyBudget {
	return &ConcurrencyBudget{perKey: map[string]int{}, keyLimit: keyLimit, totalLimit: totalLimit}
}

func (b *ConcurrencyBudget) Acquire(key string) (func(), bool) {
	if b == nil || key == "" {
		return func() {}, true
	}
	b.mu.Lock()
	if b.keyLimit > 0 && b.perKey[key] >= b.keyLimit || b.totalLimit > 0 && b.total >= b.totalLimit {
		b.mu.Unlock()
		return nil, false
	}
	b.perKey[key]++
	b.total++
	b.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			b.mu.Lock()
			defer b.mu.Unlock()
			b.perKey[key]--
			if b.perKey[key] == 0 {
				delete(b.perKey, key)
			}
			b.total--
		})
	}, true
}
