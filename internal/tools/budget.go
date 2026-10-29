package tools

import (
	"sync"
	"time"
)

// BudgetConfig sets the automatic-selection caps. Zero fields take the
// defaults below.
type BudgetConfig struct {
	UserDailySearch    int // per user per KST day (150)
	UserDailyFetch     int // (100)
	GlobalSearchPerMin int // all users, per minute (30)
	GlobalFetchPerMin  int // (60)
	GlobalDailySearch  int // all users, per KST day (1500)
	GlobalDailyFetch   int // (800)
}

func (c BudgetConfig) withDefaults() BudgetConfig {
	d := func(v *int, x int) {
		if *v <= 0 {
			*v = x
		}
	}
	d(&c.UserDailySearch, 150)
	d(&c.UserDailyFetch, 100)
	d(&c.GlobalSearchPerMin, 30)
	d(&c.GlobalFetchPerMin, 60)
	d(&c.GlobalDailySearch, 1500)
	d(&c.GlobalDailyFetch, 800)
	return c
}

// Budget counts tool use per user and globally. The day rolls over at
// midnight Asia/Seoul. Counters are in memory (reset on restart).
type Budget struct {
	mu   sync.Mutex
	cfg  BudgetConfig
	now  func() time.Time
	day  string
	user map[string]*[2]int // search, fetch
	glob [2]int
	min  [2][]time.Time
}

var kst = time.FixedZone("KST", 9*3600)

func NewBudget(c BudgetConfig) *Budget {
	return &Budget{cfg: c.withDefaults(), now: time.Now, user: map[string]*[2]int{}}
}

// Take reserves one call of kind ("search"|"fetch") for user. It returns ""
// when allowed, ErrCodeBudget when a daily cap is hit, ErrCodeRate when the
// global per-minute cap is hit. A nil Budget allows everything.
func (b *Budget) Take(user, kind string) string {
	if b == nil {
		return ""
	}
	k := 0
	userCap, globCap, minCap := b.cfg.UserDailySearch, b.cfg.GlobalDailySearch, b.cfg.GlobalSearchPerMin
	if kind == "fetch" {
		k = 1
		userCap, globCap, minCap = b.cfg.UserDailyFetch, b.cfg.GlobalDailyFetch, b.cfg.GlobalFetchPerMin
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	if d := now.In(kst).Format("2006-01-02"); d != b.day {
		b.day, b.user, b.glob = d, map[string]*[2]int{}, [2]int{}
	}
	u := b.user[user]
	if u == nil {
		u = &[2]int{}
		b.user[user] = u
	}
	if u[k] >= userCap || b.glob[k] >= globCap {
		return ErrCodeBudget
	}
	h := b.min[k][:0]
	for _, t := range b.min[k] {
		if now.Sub(t) < time.Minute {
			h = append(h, t)
		}
	}
	if len(h) >= minCap {
		b.min[k] = h
		return ErrCodeRate
	}
	b.min[k] = append(h, now)
	u[k]++
	b.glob[k]++
	return ""
}
