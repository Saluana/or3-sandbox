// Package auth authenticates requests and enforces tenant-scoped permissions.
//
// It supports both static bearer tokens and JWT-based identities, then stores
// the resolved tenant, quota, and caller identity in the request context.
package auth