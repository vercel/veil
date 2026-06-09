#!/bin/sh
set -e

REPO="vercel/veil"
# Default to a user-owned location so installation needs no sudo. Override
# via VEIL_INSTALL_DIR for a different destination.
INSTALL_DIR="${VEIL_INSTALL_DIR:-$HOME/.local/bin}"
# Which release to install. Defaults to the latest stable release; set
# VEIL_VERSION=edge for the rolling main build, or a tag like v1.2.3 to
# pin a specific one.
VEIL_VERSION="${VEIL_VERSION:-latest}"

# Detect OS
get_os() {
    case "$(uname -s)" in
        Linux)  echo "linux" ;;
        Darwin) echo "darwin" ;;
        *)      echo "Unsupported OS: $(uname -s)" >&2; exit 1 ;;
    esac
}

# Detect architecture
get_arch() {
    case "$(uname -m)" in
        x86_64)        echo "amd64" ;;
        aarch64|arm64) echo "arm64" ;;
        *)             echo "Unsupported architecture: $(uname -m)" >&2; exit 1 ;;
    esac
}

main() {
    local os arch binary_name download_url

    os=$(get_os)
    arch=$(get_arch)

    mkdir -p "$INSTALL_DIR"

    binary_name="veil-${os}-${arch}"
    # `latest` resolves through GitHub's newest-stable redirect; `edge` and
    # explicit tags (e.g. v1.2.3) download from that tag directly.
    if [ "$VEIL_VERSION" = "latest" ]; then
        download_url="https://github.com/${REPO}/releases/latest/download/${binary_name}"
    else
        download_url="https://github.com/${REPO}/releases/download/${VEIL_VERSION}/${binary_name}"
    fi

    echo "Downloading veil ${VEIL_VERSION} (${os}/${arch})..."

    curl -fsSL -o veil "${download_url}"
    chmod +x veil
    mv veil "${INSTALL_DIR}/veil"

    # Strip the macOS quarantine xattr so Gatekeeper doesn't block the
    # binary. xattr is a per-file operation and runs against a binary the
    # current user just wrote, so no elevation is needed.
    if [ "$os" = "darwin" ]; then
        xattr -cr "${INSTALL_DIR}/veil" 2>/dev/null || true
    fi

    echo "veil (${VEIL_VERSION}) installed to ${INSTALL_DIR}/veil"

    case ":$PATH:" in
        *":$INSTALL_DIR:"*) ;;
        *)
            echo
            echo "Note: ${INSTALL_DIR} is not on your \$PATH."
            echo "Add this line to your shell profile (e.g. ~/.zshrc, ~/.bashrc):"
            echo "  export PATH=\"${INSTALL_DIR}:\$PATH\""
            ;;
    esac
}

main
