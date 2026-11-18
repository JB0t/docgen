#!/usr/bin/env bash
# Build docgen from this checkout and install it (default: ~/.local/bin/docgen),
# plus its tab completion for bash (and zsh/fish when installed), so completion
# always matches the binary.
#
#   scripts/install.sh [install-dir]
#
# The version is the newest vX.Y.Z tag (v1.2.0, or v1.2.0-3-gabc1234[-dirty] for
# later commits); dev-<commit> before the first release. To install a release
# without a checkout: go install github.com/JB0t/docgen@latest
set -euo pipefail

ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
INSTALL_DIR="${1:-${DOCGEN_INSTALL_DIR:-$HOME/.local/bin}}"
case "${1:-}" in -h | --help) sed -n '2,11p' "$0"; exit 0 ;; esac

command -v go >/dev/null 2>&1 || { printf 'go is required to build docgen.\n' >&2; exit 1; }

VERSION="$(git -C "$ROOT" describe --tags --match 'v[0-9]*' --dirty 2>/dev/null || true)"
[ -n "$VERSION" ] || VERSION="dev-$(git -C "$ROOT" rev-parse --short HEAD 2>/dev/null || echo unknown)"

mkdir -p "$INSTALL_DIR"
(cd "$ROOT" && go build -trimpath -ldflags "-X github.com/JB0t/docgen/cmd.version=$VERSION" -o "$INSTALL_DIR/docgen" .)
printf 'Installed docgen %s to %s\n' "$VERSION" "$INSTALL_DIR/docgen"

DATA_DIR="${XDG_DATA_HOME:-$HOME/.local/share}"
BASH_COMPLETION="$DATA_DIR/bash-completion/completions/docgen"
mkdir -p "$(dirname "$BASH_COMPLETION")"
"$INSTALL_DIR/docgen" completion bash >"$BASH_COMPLETION"
printf 'Installed bash completion to %s\n' "$BASH_COMPLETION"
if command -v zsh >/dev/null 2>&1; then
	ZSH_COMPLETION="$DATA_DIR/zsh/site-functions/_docgen"
	mkdir -p "$(dirname "$ZSH_COMPLETION")"
	"$INSTALL_DIR/docgen" completion zsh >"$ZSH_COMPLETION"
	printf 'Installed zsh completion to %s (needs that folder in fpath before compinit)\n' "$ZSH_COMPLETION"
fi
if command -v fish >/dev/null 2>&1; then
	FISH_COMPLETION="${XDG_CONFIG_HOME:-$HOME/.config}/fish/completions/docgen.fish"
	mkdir -p "$(dirname "$FISH_COMPLETION")"
	"$INSTALL_DIR/docgen" completion fish >"$FISH_COMPLETION"
	printf 'Installed fish completion to %s\n' "$FISH_COMPLETION"
fi
case ":$PATH:" in
*":$INSTALL_DIR:"*) ;;
*) printf 'Note: %s is not on your PATH.\n' "$INSTALL_DIR" ;;
esac
