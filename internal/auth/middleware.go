package auth

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"or3-sandbox/internal/config"
	"or3-sandbox/internal/model"
	"or3-sandbox/internal/repository"
)

type tenantContextKey struct{}

type TenantContext struct {
	Tenant model.Tenant
	Quota  model.TenantQuota
}

type Middleware struct {
	store    *repository.Store
	limiters sync.Map
	rate     rate.Limit
	burst    int
}

func New(store *repository.Store, cfg config.Config) *Middleware {
	perSecond := float64(cfg.RequestRatePerMinute) / 60.0
	return &Middleware{
		store: store,
		rate:  rate.Limit(perSecond),
		burst: cfg.RequestBurst,
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
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		tenant, quota, err := m.store.AuthenticateTenant(r.Context(), config.HashToken(token))
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
			Tenant: tenant,
			Quota:  quota,
		})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func FromContext(ctx context.Context) (TenantContext, bool) {
	value, ok := ctx.Value(tenantContextKey{}).(TenantContext)
	return value, ok
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
	if value, ok := m.limiters.Load(tenantID); ok {
		return value.(*rate.Limiter)
	}
	limiter := rate.NewLimiter(m.rate, m.burst)
	actual, _ := m.limiters.LoadOrStore(tenantID, limiter)
	return actual.(*rate.Limiter)
}

func Prune(limiters *sync.Map, olderThan time.Duration) {
	_ = olderThan
	_ = limiters
}
