#!/usr/bin/env bash
# panoxy 构建脚本(单一入口):编译 CLI / 安装 / 打离线包 / 清理产物
# 用法: build.sh [命令]
#   编译(默认)   build.sh [--arch amd64|arm64|all] [--ver V0.0.1] [--prog 程序名]
#                 默认只编当前 CPU 架构;amd64 自带检测 AVX2(有→v3,无→v1)
#   安装         build.sh install [--bindir /usr/local/bin] [--ver V0.0.1] [--prog 程序名]
#                 已装机:自动用新二进制跑 redeploy —— 停服务→换 CLI→重启→重挂防火墙→健康检查
#                 未装机:仅装 CLI 到 --bindir,随后 sudo panoxy init 'SUB_URL' 完成部署
#   打包         build.sh package [all|amd64|arm64] [--arch ...] [--ver ...] [--prog 程序名] [--sub-url 订阅URL]
#                 默认只打包当前 CPU 架构;加 all(或 --arch all)打全部目标平台
#   清理         build.sh clean
#   帮助         build.sh -h|-?|--help
# 选项:
#   --arch <amd64|arm64|all>   目标架构。编译/打包默认当前平台;--arch all 打全部
#   --ver  <V0.0.1>            版本号(默认 git describe;无 git 时 V0.0.1-dev)
#   --prog <panoxy>            程序名(默认 panoxy;编译期注入,决定二进制/包名与运行期路径)
#   --sub-url <订阅URL>        打包时直连下载失败,经订阅节点建本地代理再下载
# 环境变量:
#   PROG             程序名(与 --prog 等价,默认 panoxy)
#   GOAMD64          amd64 CLI 编译档(默认自动检测 AVX2;可 GOAMD64=v1/v3/v4 强制)
#   ASSETS_SRC       本地资产目录(默认 /opt/$PROG,存在即优先复制,断网可打包)
#   PANOXY_BOOT_BIN  引导代理 CLI(默认 dist/$PROG-linux-<host_arch>;内核已内嵌,无外部 mihomo)
#   PROXY_PORT       引导代理端口(默认 33999)
# 打包流程:编译 CLI(内核内嵌)→ 资产获取(本地优先/直连/订阅代理兜底)→ 订阅泄露扫描
#   → 组装 <Prog>-V<ver>-<arch>.tar.gz + sha256(订阅 URL 永不进包)→ 清旧产物
set -euo pipefail
SELF="$(cd "$(dirname "$0")" && pwd)/$(basename "$0")"
ROOT="$(cd "$(dirname "$SELF")" && pwd)"
cd "$ROOT"

usage() { sed -n '2,/^set -euo/p' "$SELF" | sed '$d; s/^# \{0,1\}//'; exit 0; }

# ---- 全局状态(供 package 与 EXIT trap 使用) ----
SUB_URL=""; PROXYX=""; BOOT_DIRF=""; TMP=""
PROG="${PROG:-panoxy}"
PROXY_PORT="${PROXY_PORT:-33999}"
trap 'boot_proxy_stop; rm -rf "$TMP"' EXIT

host_arch() { case "$(uname -m)" in x86_64|amd64) echo amd64 ;; aarch64|arm64) echo arm64 ;; *) echo "" ;; esac; }
has_avx2()  { grep -qw avx2 /proc/cpuinfo 2>/dev/null; }
goamd64()   { if has_avx2; then echo v3; else echo v1; fi; }
envpfx()    { printf '%s' "$PROG" | tr 'a-z-' 'A-Z_'; }

# ---- 编译:默认当前架构(amd64 自动检测 AVX2),--arch 可覆盖 ----
build_cmd() {
  local ARCH="" VER=""
  while [ $# -gt 0 ]; do
    case "$1" in
      --arch) ARCH="$2"; shift 2 ;;
      --ver)  VER="$2"; shift 2 ;;
      --prog) PROG="$2"; shift 2 ;;
      -h|-\?|--help) usage ;;
      *) echo "未知参数: $1(查看用法: $0 -h)"; exit 1 ;;
    esac
  done
  [ -n "$ARCH" ] || ARCH="$(host_arch)"
  [ -n "$ARCH" ] || { echo "无法识别当前架构,请 --arch amd64|arm64|all 指定"; exit 1; }
  [ -n "$VER" ] || VER="$(git describe --tags 2>/dev/null || echo "")"
  [ -n "$VER" ] || VER="V0.0.1-dev"
  local commit="$(git rev-parse --short HEAD 2>/dev/null || echo unknown)"
  local ldflags="-s -w -X main.version=$VER -X github.com/deadship2003/panoxy/internal/constants.ProgName=$PROG -buildid="
  local targets="$ARCH"
  [ "$ARCH" = all ] && targets="amd64 arm64"
  mkdir -p dist
  echo "== 构建 $PROG $VER (commit $commit, 架构: $targets) =="
  local arch
  for arch in $targets; do
    case "$arch" in
      amd64)
        local lvl="${GOAMD64:-$(goamd64)}"
        echo "  amd64(GOAMD64=$lvl)"
        (cd src && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GOAMD64="$lvl" go build -trimpath -ldflags "$ldflags" -o ../dist/$PROG-linux-amd64 ./cmd/panoxy)
        ;;
      arm64)
        echo "  arm64"
        (cd src && CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags "$ldflags" -o ../dist/$PROG-linux-arm64 ./cmd/panoxy)
        ;;
    esac
  done
  for b in dist/$PROG-linux-amd64 dist/$PROG-linux-arm64; do
    [ -x "$b" ] && "$b" --version 2>/dev/null || true
  done
  (cd dist && sha256sum $PROG-linux-* > sha256sums.txt)
  ls -la dist
  echo "== 完成: dist/$PROG-linux-{amd64,arm64}(按需) =="
}

clean() {
  rm -rf dist/ ${PROG}-V*/
  echo "== 已清理 dist/ 与暂存目录 ${PROG}-*/ =="
}

# ---- 安装:编译当前架构后自动选择安装方式 ----
#   已装机(/etc/<prog>.yaml 与已装 CLI 均在):exec 新二进制 redeploy ——
#     停服务+清防火墙 → 就地换 CLI/单元/man → 校验重启 → 重挂防火墙 → 健康检查
#   未装机:仅安装 CLI(--bindir,默认 /usr/local/bin),提示用 init 完成部署。
# 编译始终以当前用户执行(避免 root 污染 go build 缓存),提权只发生在安装一步。
install_cmd() {
  local BINDIR="/usr/local/bin" VER=""
  while [ $# -gt 0 ]; do
    case "$1" in
      --bindir) BINDIR="$2"; shift 2 ;;
      --ver)    VER="$2"; shift 2 ;;
      --prog)   PROG="$2"; shift 2 ;;
      -h|-\?|--help) usage ;;
      *) echo "未知参数: $1(查看用法: $0 -h)"; exit 1 ;;
    esac
  done
  local HA; HA="$(host_arch)"
  [ -n "$HA" ] || { echo "无法识别当前架构,install 仅支持本机安装(交叉编译请用默认编译命令)"; exit 1; }
  build_cmd --arch "$HA" --ver "$VER" --prog "$PROG"
  local BIN="$ROOT/dist/$PROG-linux-$HA"
  [ -x "$BIN" ] || { echo "编译产物缺失: $BIN"; exit 1; }

  local SUDO=""
  [ "$(id -u)" = 0 ] || SUDO="sudo"
  srun() { if [ -n "$SUDO" ]; then "$SUDO" "$@"; else "$@"; fi; }

  # 已装机检测:与 CLI 同一套 <PROG>_CONF / <PROG>_CLI 环境覆盖
  local pfx conf cli
  pfx="$(envpfx)"
  conf="$(printenv "${pfx}_CONF" 2>/dev/null || true)"; conf="${conf:-/etc/$PROG.yaml}"
  cli="$(printenv "${pfx}_CLI" 2>/dev/null || true)";  cli="${cli:-/usr/local/bin/$PROG}"

  if [ -f "$conf" ] && [ -x "$cli" ]; then
    echo "== 已装机:经新二进制 redeploy 就地刷新(停服务 → 换 CLI → 重启 → 重挂防火墙 → 健康检查) =="
    srun "$BIN" redeploy
    echo "== 完成: $cli 已刷新(配置与订阅数据保持不动) =="
    return 0
  fi
  echo "== 未装机($conf 或 $cli 缺失):仅安装 CLI,请随后 sudo $PROG init 'SUB_URL' 完成部署 =="
  srun install -Dm755 "$BIN" "$BINDIR/$PROG"
  srun "$BINDIR/$PROG" --version 2>/dev/null || true
  echo "== 完成: $BINDIR/$PROG =="
}

# ---- 订阅引导代理:直连下载不了 GitHub 时,用订阅节点建本地代理再下 ----
# 内核已内嵌于 CLI,引导代理直接 `$PROG run`(不再依赖外部 mihomo 二进制)。
boot_proxy() {
  [ -n "$SUB_URL" ] || return 1
  local HA; HA="$(host_arch)"
  local BOOT_BIN="${PANOXY_BOOT_BIN:-$ROOT/dist/$PROG-linux-$HA}"
  [ -x "$BOOT_BIN" ] || BOOT_BIN="$(command -v "$PROG" 2>/dev/null || true)"
  [ -x "$BOOT_BIN" ] || { echo "      ⚠️ 无引导 CLI($BOOT_BIN),无法经订阅下载"; return 1; }
  local d; d="$(mktemp -d)"
  cat > "$d/boot.yaml" <<YEOF
mixed-port: $PROXY_PORT
mode: rule
log-level: warning
proxy-providers:
  boot:
    type: http
    url: "$SUB_URL"
    path: ./boot.sub.yaml
    interval: 86400
    health-check: {enable: false}
proxy-groups:
  - {name: P, type: select, use: [boot]}
rules:
  - MATCH,P
YEOF
  (cd "$d" && "$(envpfx)"_ROOT="$d" "$(envpfx)"_CONF="$d/boot.yaml" nohup "$BOOT_BIN" run > boot.log 2>&1 & echo $! > "$d/pid")
  local i ok=0
  for i in $(seq 1 25); do
    if curl -s -m 3 -x "http://127.0.0.1:$PROXY_PORT" -o /dev/null https://www.gstatic.com/generate_204; then ok=1; break; fi
    sleep 1
  done
  if [ "$ok" = 1 ]; then
    echo "$d" > "$BOOT_DIRF"
    echo "      已用订阅建立引导代理(127.0.0.1:$PROXY_PORT)"
    PROXYX="http://127.0.0.1:$PROXY_PORT"
    return 0
  fi
  echo "      ⚠️ 引导代理未就绪(订阅不可达?)"; kill "$(cat "$d/pid")" 2>/dev/null; rm -rf "$d"; return 1
}

boot_proxy_stop() {
  [ -s "$BOOT_DIRF" ] || return 0
  local d; d="$(cat "$BOOT_DIRF")"
  [ -f "$d/pid" ] && kill "$(cat "$d/pid")" 2>/dev/null
  rm -rf "$d"; : > "$BOOT_DIRF"
}

# dl:直连优先(短超时),失败且配了 SUB_URL 则经引导代理
dl() {
  curl -fsSL --connect-timeout 6 --retry 1 -o "$1" "$2" && return 0
  [ -n "$SUB_URL" ] || return 1
  [ -z "$PROXYX" ] && { boot_proxy || return 1; }
  curl -fsSL --connect-timeout 10 --retry 2 -x "$PROXYX" -o "$1" "$2"
}

# ---- 订阅泄露扫描:任何真实订阅特征都不得进入公开包 ----
leak_scan() {
  local dir="$1"
  if grep -rInE 'token=[A-Za-z0-9]{8,}|SUB_URL_PLACEHOLDER.*http|/subscribe\?|client/subscribe' "$dir" \
     --include='*.yaml' --include='*.tpl' --include='*.yml' 2>/dev/null | grep -v 'SUB_URL_PLACEHOLDER'; then
    echo "!! 检测到疑似真实订阅链接,中止打包(防止私人订阅进入公开 Release)" >&2
    exit 1
  fi
}

build_one() {
  local arch="$1"
  local pkg="${PROG}-${VER}-${arch}"
  rm -rf "$pkg"; mkdir -p "$pkg/assets/geo" "$pkg/assets/ui/official" "$pkg/assets/rule"
  cp "dist/$PROG-linux-$arch" "$pkg/$PROG"; chmod +x "$pkg/$PROG"
  cp "$TMP"/GeoIP.dat "$TMP"/GeoSite.dat "$TMP"/Country.mmdb "$pkg/assets/geo/"
  tar xzf "$TMP/ui.tgz" -C "$pkg/assets/ui/official"
  test -f "$pkg/assets/ui/official/index.html" || { echo "UI 包异常"; exit 1; }
  cp "$TMP/HyperADRules-Ads.yaml" "$pkg/assets/rule/"
  cp README.md "$pkg/"
  cp LICENSE THIRD_PARTY_NOTICES "$pkg/" 2>/dev/null || true
  cp -r LICENSES "$pkg/LICENSES" 2>/dev/null || true
  leak_scan "$pkg"
  mkdir -p dist && tar -czf "dist/$pkg.tar.gz" "$pkg"
  (cd dist && sha256sum "$pkg.tar.gz" > "$pkg.tar.gz.sha256")
  rm -rf "$pkg"   # 清暂存目录,只留 dist/ 产物
  echo "      产出: $pkg.tar.gz"
}

# cleanup_old 移除旧版本产物:dist/ 只保留本次版本,并清掉遗留的暂存目录。
# 旧版本可经 git 重编,无需在 dist/ 堆积。
cleanup_old() {
  local f d
  for f in dist/${PROG}-*.tar.gz dist/${PROG}-*.tar.gz.sha256; do
    [ -e "$f" ] || continue
    [[ "$f" == dist/${PROG}-"$VER"-*.tar.gz* ]] && continue
    rm -f "$f"
  done
  for d in ${PROG}-*; do
    [ -d "$d" ] || continue
    [[ "$d" == ${PROG}-"$VER"-* ]] && continue
    rm -rf "$d"
  done
}

package_cmd() {
  local ARCH=""
  VER=""
  while [ $# -gt 0 ]; do
    case "$1" in
      --arch) ARCH="$2"; shift 2 ;;
      --ver)  VER="$2"; shift 2 ;;
      --prog) PROG="$2"; shift 2 ;;
      --sub-url) SUB_URL="$2"; shift 2 ;;
      all|amd64|arm64) [ -n "$ARCH" ] && { echo "位置参数与 --arch 冲突: $1"; exit 1; }; ARCH="$1"; shift ;;
      -h|-\?|--help) usage ;;
      *) echo "未知参数: $1(查看用法: $0 -h)"; exit 1 ;;
    esac
  done
  [ -n "$ARCH" ] || ARCH="$(host_arch)"          # 打包默认当前 CPU 架构
  [ -n "$ARCH" ] || { echo "无法识别当前架构,请 --arch amd64|arm64|all 或位置参数 all 指定"; exit 1; }
  [ -n "$VER" ] || VER="$(git describe --tags 2>/dev/null || echo "V0.0.1-dev")"
  # 本地资产源(断网打包):存在则优先复制,缺失才联网下载
  local SRC="${ASSETS_SRC:-/opt/$PROG}"

  BOOT_DIRF="$(mktemp -d)/panoxy-boot-proxy.dir"; : > "$BOOT_DIRF" 2>/dev/null || BOOT_DIRF=/tmp/panoxy-boot-proxy.$$.dir

  echo "== [1/5] 编译(CLI, --arch $ARCH) =="
  build_cmd --arch "$ARCH" --ver "$VER" --prog "$PROG"

  echo "== [2/5] 资产获取(本地优先: $SRC;缺失才下载) =="
  TMP="$(mktemp -d)"
  # geo 三件
  local geo="https://github.com/MetaCubeX/meta-rules-dat/releases/download/latest"
  local f
  for f in GeoIP.dat GeoSite.dat Country.mmdb; do
    if [ -f "$SRC/$f" ]; then cp "$SRC/$f" "$TMP/$f"; echo "      本地: $f"
    else dl "$TMP/$f" "$geo/$(echo $f | tr 'A-Z' 'a-z' | sed 's/\.dat$/.dat/;s/country\.mmdb/country.mmdb/')" || true; fi
  done
  # 广告规则
  if [ -f "$SRC/rule_provider/HyperADRules-Ads.yaml" ]; then cp "$SRC/rule_provider/HyperADRules-Ads.yaml" "$TMP/"; echo "      本地: HyperADRules-Ads.yaml"
  else dl "$TMP/HyperADRules-Ads.yaml" "https://github.com/Lynricsy/HyperADRules/releases/latest/download/hyper_adrules_ads_clash.yaml" || true; fi
  # 面板
  if [ -d "$SRC/ui/official" ] && [ -f "$SRC/ui/official/index.html" ]; then
    (cd "$SRC/ui/official" && tar czf "$TMP/ui.tgz" .); echo "      本地: metacubexd UI"
  else dl "$TMP/ui.tgz" "https://github.com/MetaCubeX/metacubexd/releases/latest/download/compressed-dist.tgz" || true; fi
  [ -s "$TMP/Country.mmdb" ] && [ -s "$TMP/HyperADRules-Ads.yaml" ] && [ -s "$TMP/ui.tgz" ] || { echo "geo/规则/UI 资产不完整(本地与网络均不可得)"; exit 1; }

  echo "== [3/5] 订阅泄露扫描 =="
  leak_scan .

  echo "== [4/5] 组装 =="
  case "$ARCH" in
    amd64|arm64) build_one "$ARCH" ;;
    all) build_one amd64; build_one arm64 ;;
    *) echo "--arch 只能是 amd64|arm64|all"; exit 1 ;;
  esac

  echo "== [5/5] 完成 =="
  cleanup_old
  ls -la dist/${PROG}-*.tar.gz* 2>/dev/null | tail -4
}

case "${1:-}" in
  -h|-\?|--help) usage ;;
  clean)  clean ;;
  install) shift; install_cmd "$@" ;;
  package) shift; package_cmd "$@" ;;
  *)      build_cmd "$@" ;;
esac
