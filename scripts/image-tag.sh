#!/usr/bin/env bash
# Normalize VERSION for GHCR release tags.
set -euo pipefail

version=$(tr -d '[:space:]')
version="${version#[vV]}"

if [ -z "$version" ] || [ "$version" = "dev" ]; then
  printf '%s\n' latest
else
  printf '%s\n' "$version"
fi
