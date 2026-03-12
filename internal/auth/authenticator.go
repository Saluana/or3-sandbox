package auth

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	jwt "github.com/golang-jwt/jwt/v5"

	"or3-sandbox/internal/config"
	"or3-sandbox/internal/model"
	"or3-sandbox/internal/repository"
)

type Authenticator interface {
	Authenticate(ctx context.Context, token string) (Identity, model.Tenant, model.TenantQuota, error)
}

type staticAuthenticator struct {
	store *repository.Store
}

type jwtAuthenticator struct {
	store        *repository.Store
	issuer       string
	audience     string
	secrets      []string
	loadErr      error
	defaultQuota model.TenantQuota
}

type jwtClaims struct {
	TenantID         string   `json:"tenant_id"`
	Roles            []string `json:"roles"`
	ServiceAccountID string   `json:"service_account_id,omitempty"`
	Scope            []string `json:"scope,omitempty"`
	Service          bool     `json:"service"`
	jwt.RegisteredClaims
}

func newAuthenticator(store *repository.Store, cfg config.Config) Authenticator {
	switch cfg.AuthMode {
	case "jwt-hs256":
		secrets, err := loadSecretValues(cfg.AuthJWTSecretPaths)
		return &jwtAuthenticator{
			store:        store,
			issuer:       cfg.AuthJWTIssuer,
			audience:     cfg.AuthJWTAudience,
			secrets:      secrets,
			loadErr:      err,
			defaultQuota: cfg.DefaultQuota,
		}
	default:
		return &staticAuthenticator{store: store}
	}
}

func (a *staticAuthenticator) Authenticate(ctx context.Context, token string) (Identity, model.Tenant, model.TenantQuota, error) {
	tenant, quota, err := a.store.AuthenticateTenant(ctx, config.HashToken(token))
	if err != nil {
		return Identity{}, model.Tenant{}, model.TenantQuota{}, err
	}
	return Identity{
		Subject:    tenant.ID,
		TenantID:   tenant.ID,
		Roles:      []string{"operator"},
		AuthMethod: "static",
	}, tenant, quota, nil
}

func (a *jwtAuthenticator) Authenticate(ctx context.Context, token string) (Identity, model.Tenant, model.TenantQuota, error) {
	if a.loadErr != nil {
		return Identity{}, model.Tenant{}, model.TenantQuota{}, a.loadErr
	}
	var err error
	secrets := a.secrets
	if len(secrets) == 0 {
		return Identity{}, model.Tenant{}, model.TenantQuota{}, fmt.Errorf("no jwt secrets loaded")
	}
	var claims jwtClaims
	var parseErr error
	for _, secret := range secrets {
		claims = jwtClaims{}
		_, parseErr = jwt.ParseWithClaims(token, &claims, func(parsed *jwt.Token) (any, error) {
			if parsed.Method.Alg() != jwt.SigningMethodHS256.Alg() {
				return nil, fmt.Errorf("unexpected signing method %s", parsed.Method.Alg())
			}
			return []byte(secret), nil
		}, jwt.WithIssuer(a.issuer), jwt.WithAudience(a.audience))
		if parseErr == nil {
			break
		}
	}
	if parseErr != nil {
		return Identity{}, model.Tenant{}, model.TenantQuota{}, parseErr
	}
	if strings.TrimSpace(claims.TenantID) == "" {
		return Identity{}, model.Tenant{}, model.TenantQuota{}, fmt.Errorf("jwt claim tenant_id is required")
	}
	if claims.RegisteredClaims.Subject == "" {
		claims.RegisteredClaims.Subject = claims.TenantID
	}
	roles := append([]string(nil), claims.Roles...)
	if (claims.Service || claims.ServiceAccountID != "") && len(roles) == 0 {
		roles = []string{"service-account"}
	}
	tenant := model.Tenant{ID: claims.TenantID, Name: claims.TenantID}
	quota, err := a.store.GetQuota(ctx, claims.TenantID)
	if errors.Is(err, repository.ErrNotFound) {
		if err := a.store.EnsureTenantQuota(ctx, tenant, a.defaultQuota, config.HashToken("jwt:"+claims.TenantID)); err != nil {
			return Identity{}, model.Tenant{}, model.TenantQuota{}, err
		}
		quota, err = a.store.GetQuota(ctx, claims.TenantID)
	}
	if err != nil {
		return Identity{}, model.Tenant{}, model.TenantQuota{}, err
	}
	scope := append([]string(nil), claims.Scope...)
	serviceAccountID := strings.TrimSpace(claims.ServiceAccountID)
	if serviceAccountID != "" {
		account, err := a.store.GetServiceAccount(ctx, serviceAccountID)
		if err != nil {
			return Identity{}, model.Tenant{}, model.TenantQuota{}, err
		}
		if account.TenantID != claims.TenantID {
			return Identity{}, model.Tenant{}, model.TenantQuota{}, fmt.Errorf("service account tenant mismatch")
		}
		if account.Disabled || account.RevokedAt != nil {
			return Identity{}, model.Tenant{}, model.TenantQuota{}, fmt.Errorf("service account revoked")
		}
		if account.ExpiresAt != nil && time.Now().UTC().After(account.ExpiresAt.UTC()) {
			return Identity{}, model.Tenant{}, model.TenantQuota{}, fmt.Errorf("service account expired")
		}
		if len(scope) == 0 {
			scope = append(scope, account.Scopes...)
		} else if !scopesSubset(scope, account.Scopes) {
			return Identity{}, model.Tenant{}, model.TenantQuota{}, fmt.Errorf("service account scope exceeds registered scope")
		}
	}
	return Identity{
		Subject:          claims.RegisteredClaims.Subject,
		TenantID:         claims.TenantID,
		Roles:            roles,
		Scopes:           scope,
		ServiceAccountID: serviceAccountID,
		IsService:        claims.Service || serviceAccountID != "",
		AuthMethod:       "jwt-hs256",
	}, tenant, quota, nil
}

func loadSecretValues(paths []string) ([]string, error) {
	values := make([]string, 0, len(paths))
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		secret := strings.TrimSpace(string(data))
		if secret == "" {
			return nil, fmt.Errorf("secret file %s is empty", path)
		}
		values = append(values, secret)
	}
	if len(values) == 0 {
		return nil, fmt.Errorf("no jwt secrets loaded")
	}
	return values, nil
}

func scopesSubset(requested, allowed []string) bool {
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, value := range allowed {
		allowedSet[strings.TrimSpace(value)] = struct{}{}
	}
	for _, value := range requested {
		if _, ok := allowedSet[strings.TrimSpace(value)]; !ok {
			return false
		}
	}
	return true
}
