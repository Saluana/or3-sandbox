// Package service orchestrates sandbox lifecycle, policy, persistence, and
// runtime backends.
//
// It is the main application layer: API handlers delegate to Service methods,
// which in turn enforce policy, call runtimes, and persist the resulting state.
package service