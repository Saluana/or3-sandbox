// Package config loads, normalizes, and validates daemon configuration from
// flags and environment variables.
//
// The package centralizes deployment-mode policy, runtime selection, and
// default quota handling so callers can operate on a single validated [Config].
package config