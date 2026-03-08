package auth

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/time/rate"

	"or3-sandbox/internal/config"
	"or3-sandbox/internal/repository"
)

type Middleware struct {
	store         *repository.Store
	authenticator Authenticator
	limiters      sync.Map
	lastPruneUnix atomic.Int64
	rate          rate.Limit
	burst         int
}

type tenantLimiter struct {
	limiter  *rate.Limiter
	lastSeen atomic.Int64
}

func New(store *repository.Store, cfg config.Config) *Middleware {
	perSecond := float64(cfg.RequestRatePerMinute) / 60.0
	return &Middleware{
		store:         store,
		authenticator: newAuthenticator(store, cfg),
		rate:          rate.Limit(perSecond),
		burst:         cfg.RequestBurst,
	}
}

func (m *Middleware) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			next.ServeHTTP(w, r)
			return
		}
		token, err := bearerToken(r.Header.Get("Authorization"))
		if err != nil {
			if isTunnelProxyPath(r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		identity, tenant, quota, err := m.authenticator.Authenticate(r.Context(), token)
		if err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		limiter := m.limiterFor(tenant.ID)
		if !limiter.Allow() {
			http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
			return
		}
		ctx := context.WithValue(r.Context(), tenantContextKey{}, TenantContext{
			Tenant:   tenant,
			Quota:    quota,
			Identity: identity,
		})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func isTunnelProxyPath(path string) bool {
	return strings.HasPrefix(path, "/v1/tunnels/") && strings.Contains(path, "/proxy")
}

func bearerToken(header string) (string, error) {
	if header == "" {
		return "", errors.New("missing authorization header")
	}
	parts := strings.SplitN(header, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || strings.TrimSpace(parts[1]) == "" {
		return "", errors.New("invalid authorization header")
	}
	return strings.TrimSpace(parts[1]), nil
}

func (m *Middleware) limiterFor(tenantID string) *rate.Limiter {
	now := time.Now().UnixNano()
	if value, ok := m.limiters.Load(tenantID); ok {
		entry := value.(*tenantLimiter)
		entry.lastSeen.Store(now)
		m.maybePrune(now)
		return entry.limiter
	}
	entry := &tenantLimiter{limiter: rate.NewLimiter(m.rate, m.burst)}
	entry.lastSeen.Store(now)
	actual, _ := m.limiters.LoadOrStore(tenantID, entry)
	stored := actual.(*tenantLimiter)
	stored.lastSeen.Store(now)
	m.maybePrune(now)
	return stored.limiter
}

func Prune(limiters *sync.Map, olderThan time.Duration) {
	if limiters == nil || olderThan <= 0 {
		return
	}
	cutoff := time.Now().Add(-olderThan).UnixNano()
	limiters.Range(func(key, value any) bool {
		entry, ok := value.(*tenantLimiter)
		if !ok || entry.lastSeen.Load() < cutoff {
			limiters.Delete(key)
		}
		return true
	})
}

func (m *Middleware) maybePrune(nowUnixNano int64) {
	last := m.lastPruneUnix.Load()
	if last != 0 && nowUnixNano-last < int64(5*time.Minute) {
		return
	}
	if !m.lastPruneUnix.CompareAndSwap(last, nowUnixNano) {
		return
	}
	Prune(&m.limiters, 15*time.Minute)
}
