package auth

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	jwt "github.com/golang-jwt/jwt/v5"

	"or3-sandbox/internal/config"
	"or3-sandbox/internal/db"
	"or3-sandbox/internal/model"
	"or3-sandbox/internal/repository"
)

func TestJWTAuthenticatorAuthenticatesServiceIdentity(t *testing.T) {
	store := newAuthTestStore(t)
	secretPath := filepath.Join(t.TempDir(), "jwt.secret")
	if err := os.WriteFile(secretPath, []byte("super-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	authenticator := newAuthenticator(store, config.Config{
		AuthMode:           "jwt-hs256",
		AuthJWTIssuer:      "issuer.example",
		AuthJWTAudience:    "sandbox-api",
		AuthJWTSecretPaths: []string{secretPath},
	}).(*jwtAuthenticator)
	token := signedTestJWT(t, "super-secret", jwt.MapClaims{
		"iss":       "issuer.example",
		"aud":       "sandbox-api",
		"sub":       "svc-buildkite",
		"tenant_id": "tenant-a",
		"service":   true,
		"exp":       time.Now().Add(time.Hour).Unix(),
	})

	identity, tenant, quota, err := authenticator.Authenticate(context.Background(), token)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	if !identity.IsService {
		t.Fatal("expected service identity")
	}
	if identity.Subject != "svc-buildkite" || tenant.ID != "tenant-a" || quota.TenantID != "tenant-a" {
		t.Fatalf("unexpected auth result: identity=%+v tenant=%+v quota=%+v", identity, tenant, quota)
	}
	ctx := context.WithValue(context.Background(), tenantContextKey{}, TenantContext{Identity: identity})
	if err := Require(ctx, PermissionAdminInspect); err != nil {
		t.Fatalf("expected service identity to have admin inspect permission, got %v", err)
	}
}

func TestJWTAuthenticatorSeedsDefaultQuotaForNewTenant(t *testing.T) {
	store := newEmptyAuthTestStore(t)
	secretPath := filepath.Join(t.TempDir(), "jwt.secret")
	if err := os.WriteFile(secretPath, []byte("super-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	defaultQuota := model.TenantQuota{
		MaxSandboxes:            4,
		MaxRunningSandboxes:     2,
		MaxConcurrentExecs:      3,
		MaxTunnels:              1,
		MaxCPUCores:             model.CPUCores(2),
		MaxMemoryMB:             2048,
		MaxStorageMB:            4096,
		AllowTunnels:            true,
		DefaultTunnelAuthMode:   "token",
		DefaultTunnelVisibility: "private",
	}
	authenticator := newAuthenticator(store, config.Config{
		AuthMode:           "jwt-hs256",
		AuthJWTIssuer:      "issuer.example",
		AuthJWTAudience:    "sandbox-api",
		AuthJWTSecretPaths: []string{secretPath},
		DefaultQuota:       defaultQuota,
	}).(*jwtAuthenticator)
	token := signedTestJWT(t, "super-secret", jwt.MapClaims{
		"iss":       "issuer.example",
		"aud":       "sandbox-api",
		"sub":       "tenant-c-subject",
		"tenant_id": "tenant-c",
		"roles":     []string{"developer"},
		"exp":       time.Now().Add(time.Hour).Unix(),
	})

	identity, tenant, quota, err := authenticator.Authenticate(context.Background(), token)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	if identity.TenantID != "tenant-c" || tenant.ID != "tenant-c" {
		t.Fatalf("unexpected tenant identity: identity=%+v tenant=%+v", identity, tenant)
	}
	if quota.MaxSandboxes != defaultQuota.MaxSandboxes || quota.MaxCPUCores != defaultQuota.MaxCPUCores {
		t.Fatalf("expected default quota to be provisioned, got %+v", quota)
	}

	storedQuota, err := store.GetQuota(context.Background(), "tenant-c")
	if err != nil {
		t.Fatalf("get quota: %v", err)
	}
	if storedQuota.MaxMemoryMB != defaultQuota.MaxMemoryMB {
		t.Fatalf("expected stored default quota, got %+v", storedQuota)
	}
}

func TestJWTAuthenticatorRejectsInvalidToken(t *testing.T) {
	store := newAuthTestStore(t)
	secretPath := filepath.Join(t.TempDir(), "jwt.secret")
	if err := os.WriteFile(secretPath, []byte("super-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	authenticator := newAuthenticator(store, config.Config{
		AuthMode:           "jwt-hs256",
		AuthJWTIssuer:      "issuer.example",
		AuthJWTAudience:    "sandbox-api",
		AuthJWTSecretPaths: []string{secretPath},
	})
	if _, _, _, err := authenticator.Authenticate(context.Background(), "not-a-jwt"); err == nil {
		t.Fatal("expected invalid token rejection")
	}
}

func newEmptyAuthTestStore(t *testing.T) *repository.Store {
	t.Helper()
	root := t.TempDir()
	sqlDB, err := db.Open(context.Background(), filepath.Join(root, "sandbox.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	return repository.New(sqlDB)
}

func newAuthTestStore(t *testing.T) *repository.Store {
	t.Helper()
	store := newEmptyAuthTestStore(t)
	quota := model.TenantQuota{
		MaxSandboxes:            8,
		MaxRunningSandboxes:     8,
		MaxConcurrentExecs:      8,
		MaxTunnels:              8,
		MaxCPUCores:             model.CPUCores(8),
		MaxMemoryMB:             8192,
		MaxStorageMB:            8192,
		AllowTunnels:            true,
		DefaultTunnelAuthMode:   "token",
		DefaultTunnelVisibility: "private",
	}
	if err := store.SeedTenants(context.Background(), []config.TenantConfig{{ID: "tenant-a", Name: "Tenant A", Token: "token-a"}}, quota); err != nil {
		t.Fatal(err)
	}
	return store
}

func signedTestJWT(t *testing.T, secret string, claims jwt.MapClaims) string {
	t.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString([]byte(secret))
	if err != nil {
		t.Fatal(err)
	}
	return signed
}
