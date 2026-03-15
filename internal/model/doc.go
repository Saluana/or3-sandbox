// Package model defines the domain and transport types shared across the API,
// service, repository, and runtime layers.
//
// The package keeps cross-package contracts in one place so runtime adapters,
// persistence code, and HTTP handlers can evolve without duplicating resource
// shapes or lifecycle enums.
package model