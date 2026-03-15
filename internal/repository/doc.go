// Package repository persists sandbox control-plane state in SQLite.
//
// It is the single persistence boundary for tenants, quotas, sandboxes,
// executions, snapshots, tunnels, and related observability records.
package repository