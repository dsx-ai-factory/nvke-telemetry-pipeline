#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
# Install the pinned dev loop tools into dev/.bin, skipping any already at the pinned version.
set -euo pipefail

KIND_VERSION=v0.33.0
KUBECTL_VERSION=v1.35.9
HELM_VERSION=v4.3.2
TILT_VERSION=v0.37.8

bin_dir="$(cd "$(dirname "$0")" && pwd)/.bin"

os=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$os" in
    darwin | linux) ;;
    *) echo "Unsupported OS: $os" >&2; exit 1 ;;
esac
case "$(uname -m)" in
    x86_64 | amd64) arch=amd64 ;;
    arm64 | aarch64) arch=arm64 ;;
    *) echo "Unsupported architecture: $(uname -m)" >&2; exit 1 ;;
esac

mkdir -p "$bin_dir"
tmp=$(mktemp -d "$bin_dir/.tmp.XXXXXX")
trap 'rm -rf "$tmp"' EXIT

sha256() {
    if command -v sha256sum >/dev/null; then sha256sum "$1"; else shasum -a 256 "$1"; fi | cut -d ' ' -f 1
}

# installed <tool> <version>
installed() {
    local current
    [ -x "$bin_dir/$1" ] || return 1
    case "$1" in
        kind) current=$("$bin_dir/kind" version 2>/dev/null | awk '{ print $2 }') ;;
        kubectl) current=$("$bin_dir/kubectl" version --client 2>/dev/null | awk '/^Client Version:/ { print $3 }') ;;
        helm) current=$("$bin_dir/helm" version --template '{{.Version}}' 2>/dev/null) ;;
        tilt) current=$("$bin_dir/tilt" version 2>/dev/null | cut -d , -f 1) ;;
    esac
    [ "$current" = "$2" ]
}

# download <url> <expected sha256>
download() {
    curl -fsSL --retry 3 -o "$tmp/download" "$1"
    if [ "$(sha256 "$tmp/download")" != "$2" ]; then
        echo "Checksum mismatch for $1" >&2
        exit 1
    fi
}

# place <file> <tool>
place() {
    chmod 0755 "$1"
    mv -f "$1" "$bin_dir/$2"
}

if ! installed kind "$KIND_VERSION"; then
    echo "Installing kind $KIND_VERSION into dev/.bin..."
    url="https://github.com/kubernetes-sigs/kind/releases/download/$KIND_VERSION/kind-$os-$arch"
    download "$url" "$(curl -fsSL --retry 3 "$url.sha256sum" | cut -d ' ' -f 1)"
    place "$tmp/download" kind
fi

if ! installed kubectl "$KUBECTL_VERSION"; then
    echo "Installing kubectl $KUBECTL_VERSION into dev/.bin..."
    url="https://dl.k8s.io/release/$KUBECTL_VERSION/bin/$os/$arch/kubectl"
    download "$url" "$(curl -fsSL --retry 3 "$url.sha256" | cut -d ' ' -f 1)"
    place "$tmp/download" kubectl
fi

if ! installed helm "$HELM_VERSION"; then
    echo "Installing helm $HELM_VERSION into dev/.bin..."
    url="https://get.helm.sh/helm-$HELM_VERSION-$os-$arch.tar.gz"
    download "$url" "$(curl -fsSL --retry 3 "$url.sha256sum" | cut -d ' ' -f 1)"
    tar -xzf "$tmp/download" -C "$tmp" "$os-$arch/helm"
    place "$tmp/$os-$arch/helm" helm
fi

if ! installed tilt "$TILT_VERSION"; then
    echo "Installing tilt $TILT_VERSION into dev/.bin..."
    tilt_os=$os
    tilt_arch=$arch
    [ "$os" = darwin ] && tilt_os=mac
    [ "$arch" = amd64 ] && tilt_arch=x86_64
    file="tilt.${TILT_VERSION#v}.$tilt_os.$tilt_arch.tar.gz"
    base="https://github.com/tilt-dev/tilt/releases/download/$TILT_VERSION"
    download "$base/$file" "$(curl -fsSL --retry 3 "$base/checksums.txt" | awk -v f="$file" '$2 == f { print $1 }')"
    tar -xzf "$tmp/download" -C "$tmp" tilt
    place "$tmp/tilt" tilt
fi
