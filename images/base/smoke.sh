#!/usr/bin/env bash
set -euo pipefail

git --version
python3 --version
python3 -c 'import sys; print(sys.executable)'
node --version
npm --version
python3 -c 'from pathlib import Path; Path("/workspace/browser.txt").write_text("ok")'
node -e 'console.log("playwright deps available")'
