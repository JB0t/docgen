#!/usr/bin/env bash
# Deploys the stack.
# Usage: deploy.sh [--dry-run] [-v|--verbose]

# Prints a message.
log() { echo "$*"; }

case "$1" in
  --dry-run) DRY=1 ;;
  -v|--verbose) set -x ;;
esac
echo "${DEPLOY_ENV:-dev}"
