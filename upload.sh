#!/usr/bin/env bash

set -e

SCRIPT_DIR="$(dirname "$0")"

"$SCRIPT_DIR/.venv/bin/python" "$SCRIPT_DIR/deploy.py"
