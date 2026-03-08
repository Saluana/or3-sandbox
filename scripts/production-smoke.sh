#!/bin/sh
set -eu

GO_BIN="${GO_BIN:-}"
if [ -z "$GO_BIN" ]; then
	if command -v go >/dev/null 2>&1; then
		GO_BIN="$(command -v go)"
	elif [ -x /usr/local/go/bin/go ]; then
		GO_BIN=/usr/local/go/bin/go
	else
		echo "go binary not found; set GO_BIN or install Go" >&2
		exit 127
	fi
fi

cd "$(dirname "$0")/.."

exec "$GO_BIN" test \
	./internal/config \
	./internal/auth \
	./internal/service \
	./internal/api \
	./cmd/sandboxctl
