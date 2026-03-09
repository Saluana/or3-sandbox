#!/usr/bin/env bash
set -euo pipefail

git --version
python3 --version
python3 -c 'import sys; print(sys.executable)'
if command -v docker >/dev/null 2>&1; then
	echo "docker should not be installed in the runtime image" >&2
	exit 1
fi
python3 -c 'from pathlib import Path; Path("/workspace/browser.txt").write_text("ok")'
bash -lc 'printf "runtime-smoke\n" > /workspace/runtime.txt && cat /workspace/runtime.txt'
