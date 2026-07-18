#!/usr/bin/env bash
# 从根目录 VERSION 同步到发布展示与 Compose 默认镜像（唯一手改源：VERSION）
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
VERSION="$(tr -d '[:space:]' < "$ROOT/VERSION")"
VERSION="${VERSION#v}"
VERSION="${VERSION#V}"
if [ -z "$VERSION" ]; then
  echo "VERSION empty" >&2
  exit 1
fi

python3 - "$ROOT" "$VERSION" <<'PY'
import pathlib, re, sys
root = pathlib.Path(sys.argv[1])
version = sys.argv[2]

def write(path: pathlib.Path, text: str) -> None:
    path.write_text(text, encoding="utf-8", newline="\n")
    print(f"{path.relative_to(root)} -> {version}")

pkg = root / "frontend" / "package.json"
text = pkg.read_text(encoding="utf-8")
new = re.sub(r'"version"\s*:\s*"[^"]*"', f'"version": "{version}"', text, count=1)
if new != text:
    write(pkg, new)
else:
    print(f"package.json already at {version}")

main = root / "backend" / "cmd" / "grok2api" / "main.go"
text = main.read_text(encoding="utf-8")
new = re.sub(r"// @version\s+\S+", f"// @version {version}", text, count=1)
if new != text:
    write(main, new)

# 与 swag 生成格式对齐：yaml 无引号；docs.go 使用 swag 默认空格缩进
docs_go = root / "backend" / "docs" / "docs.go"
if docs_go.exists():
    text = docs_go.read_text(encoding="utf-8")
    new = re.sub(r'(\t| {2,})Version:\s+"[^"]*"', rf'\1Version:          "{version}"', text, count=1)
    if new != text:
        write(docs_go, new)

swagger_yaml = root / "backend" / "docs" / "swagger.yaml"
if swagger_yaml.exists():
    text = swagger_yaml.read_text(encoding="utf-8")
    # swag 输出: 缩进后的 version: 3.0.0 （无引号）
    new = re.sub(r'(?m)^(\s*)version:\s*.*$', rf"\1version: {version}", text, count=1)
    if new != text:
        write(swagger_yaml, new)

swagger_json = root / "backend" / "docs" / "swagger.json"
if swagger_json.exists():
    text = swagger_json.read_text(encoding="utf-8")
    new = re.sub(r'"version":\s*"[^"]*"', f'"version": "{version}"', text, count=1)
    if new != text:
        write(swagger_json, new)

compose = root / "docker-compose.yml"
text = compose.read_text(encoding="utf-8")
lines = text.splitlines(keepends=True)
compose_marker = "GROK2API_IMAGE:-ghcr.io/jiujiu532/grok2api-r0:"
for index, line in enumerate(lines):
    if compose_marker not in line:
        continue
    ending = "\r\n" if line.endswith("\r\n") else "\n"
    lines[index] = f'    image: "${{GROK2API_IMAGE:-ghcr.io/jiujiu532/grok2api-r0:{version}}}"{ending}'
    break
else:
    raise SystemExit("docker-compose.yml default image was not updated")
write(compose, "".join(lines))

print(f"OK: app version = {version} (source: VERSION)")
PY
