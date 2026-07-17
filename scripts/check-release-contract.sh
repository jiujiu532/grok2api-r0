#!/usr/bin/env bash
# Verify the VERSION-to-GHCR release contract without Docker.
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
expected_tag=$(bash "$root/scripts/image-tag.sh" < "$root/VERSION")

fail() {
  printf 'release contract check failed: %s\n' "$1" >&2
  exit 1
}

[ "$expected_tag" = "$(printf '%s' "$expected_tag" | tr -d '[:space:]')" ] || fail "normalized tag must not contain whitespace"
printf '%s' "$expected_tag" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+([-.][0-9A-Za-z.-]+)?$' || fail "VERSION must normalize to a release tag"
grep -Fq 'bash "$script_dir/image-tag.sh"' "$root/scripts/install.sh" || fail "installer must normalize VERSION with image-tag.sh"
grep -Fq 'bash scripts/image-tag.sh < VERSION' "$root/.github/workflows/ghcr-image.yml" || fail "workflow must normalize VERSION with image-tag.sh"
grep -Fq "grok2api-r0:${expected_tag}" "$root/docker-compose.yml" || fail "compose must default to the normalized release tag"
grep -Fq "\"version\": \"${expected_tag}\"" "$root/frontend/package.json" || fail "frontend package version must be synchronized"
grep -Fq "// @version ${expected_tag}" "$root/backend/cmd/grok2api/main.go" || fail "Swagger annotation must be synchronized"
grep -Fq 'type=raw,value=${{ steps.version.outputs.tag }}-${{ matrix.arch }}' "$root/.github/workflows/ghcr-image.yml" || fail "workflow must publish release architecture tags"
grep -Fq 'type=raw,value=${{ steps.version.outputs.tag }}' "$root/.github/workflows/ghcr-image.yml" || fail "workflow must publish the release manifest tag"
grep -Fq "type=raw,value=latest,enable=\${{ github.ref == 'refs/heads/main' }}" "$root/.github/workflows/ghcr-image.yml" || fail "latest must be limited to main"
grep -Fq 'secureCookies: ${SECURE_COOKIES}' "$root/scripts/install.sh" || fail "installer must select secure cookie mode"
grep -Fq '是否通过 HTTPS 反向代理提供管理端" "Y"' "$root/scripts/install.sh" || fail "HTTPS reverse proxy must be the cookie default"
grep -Fq 'GROK2API_BOOTSTRAP_PASSWORD' "$root/scripts/install.sh" || fail "bootstrap password must not be a Python argument"
! grep -Fq 'log "  密码:       $admin_pass"' "$root/scripts/install.sh" || fail "installer must not print administrator passwords"

umask_line=$(grep -nF 'umask 077' "$root/scripts/install.sh" | head -1 | cut -d: -f1)
mkdir_line=$(grep -nF 'mkdir -p "$INSTALL_DIR"' "$root/scripts/install.sh" | head -1 | cut -d: -f1)
[ -n "$umask_line" ] && [ "$umask_line" -lt "$mkdir_line" ] || fail "umask must be set before secret files"

tmp_dir=$(mktemp -d)
trap 'rm -rf "$tmp_dir"' EXIT
generated_output=$(
  (
    export GROK2API_INSTALL_DIR="$tmp_dir"
    source "$root/scripts/install.sh"
    CONFIG_FILE="$tmp_dir/config.yaml"
    SECURE_COOKIES=true
    umask 077
    generate_config_yaml test-jwt test-encryption-key test-admin test-password
  )
)
[ -z "$generated_output" ] || fail "config generation must not print secrets"
[ "$(stat -c '%a' "$tmp_dir/config.yaml")" = "600" ] || fail "fresh config must be mode 600"
grep -Fq 'secureCookies: true' "$tmp_dir/config.yaml" || fail "HTTPS configuration must use secure cookies"

printf 'release contract check passed: %s\n' "$expected_tag"
