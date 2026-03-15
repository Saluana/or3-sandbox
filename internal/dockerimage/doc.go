// Package dockerimage resolves curated metadata for Docker-backed sandbox base
// images.
//
// It prefers built-in repository mappings and can optionally fall back to image
// labels when the Docker CLI is available.
package dockerimage