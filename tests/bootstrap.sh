#!/usr/bin/env bash
set -Eeuo pipefail
root="$(cd -- "$(dirname -- "$0")/.." && pwd -P)"
pkg="${1:?Path to public runtime is required}"
scratch="$(mktemp -d)"
trap 'rm -rf -- "$scratch"' EXIT
bash -n "$root/sb.sh"
shellcheck "$root/sb.sh"
bash "$root/sb.sh" help >/dev/null
bash "$root/sb.sh" version | grep -F '1.11.2'
if setsid bash "$root/sb.sh" </dev/null >/dev/null 2>&1; then exit 1; fi
if setsid bash "$root/sb.sh" unknown </dev/null >/dev/null 2>&1; then exit 1; fi
cp -- "$pkg" "$scratch/Fresh package & test.tar.gz"
(cd / && SBOX_INSTALL_ARCHIVE="$scratch/Fresh package & test.tar.gz" bash "$root/sb.sh" verify)
printf x >> "$scratch/Fresh package & test.tar.gz"
if SBOX_INSTALL_ARCHIVE="$scratch/Fresh package & test.tar.gz" bash "$root/sb.sh" verify >/dev/null 2>&1; then exit 1; fi
printf 'Bootstrap checks passed.\n'
