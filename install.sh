#!/bin/sh
set -e

REPO="vercel/veil"
INSTALL_DIR="/usr/local/bin"

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

    # Cache sudo credentials up front so the prompt happens before the
    # download — avoids issues when the script is piped via curl | sh.
    if [ ! -w "$INSTALL_DIR" ]; then
        echo "Installation to ${INSTALL_DIR} requires sudo access."
        sudo -v
    fi

    binary_name="veil-${os}-${arch}"
    download_url="https://github.com/${REPO}/releases/download/edge/${binary_name}"

    echo "Downloading veil edge (${os}/${arch})..."

    curl -fsSL -o veil "${download_url}"
    chmod +x veil

    if [ -w "$INSTALL_DIR" ]; then
        mv veil "${INSTALL_DIR}/veil"
    else
        sudo mv veil "${INSTALL_DIR}/veil"
    fi

    if [ "$os" = "darwin" ]; then
        sudo xattr -cr "${INSTALL_DIR}/veil" 2>/dev/null || true
    fi

    echo "veil (edge) installed to ${INSTALL_DIR}/veil"
}

main
