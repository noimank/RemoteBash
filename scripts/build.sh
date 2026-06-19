#!/usr/bin/env bash
#
# RemoteBash 全平台编译脚本 (Linux/macOS)
#
# 交叉编译 Go 项目为所有主流平台生成静态二进制文件，
# 输出到 build/ 目录并生成 SHA256 校验文件。
#
# 用法:
#   ./scripts/build.sh                          # 全平台编译
#   ./scripts/build.sh v2.1.0                   # 指定版本号
#   ./scripts/build.sh v2.1.0 build             # 指定版本号和输出目录
#   ./scripts/build.sh v2.1.0 build "linux/amd64,windows/amd64"  # 指定平台
#
set -euo pipefail

# ─── 参数 ──────────────────────────────────────────────
VERSION="${1:-}"
OUTPUT_DIR="${2:-build}"
PLATFORM_FILTER="${3:-}"

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

cd "$PROJECT_ROOT"

# 版本号：参数 > git tag > "dev"
if [[ -z "$VERSION" ]]; then
    VERSION=$(git describe --tags --abbrev=0 2>/dev/null || echo "dev")
fi

# 颜色
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
NC='\033[0m' # No Color

echo -e "${CYAN}=== RemoteBash 编译脚本 ===${NC}"
echo "版本: $VERSION"
echo "输出目录: $OUTPUT_DIR"
echo ""

# ─── 平台定义 ──────────────────────────────────────────
# 格式: GOOS/GOARCH[:GOARM]|suffix|ext
ALL_PLATFORMS=(
    "linux/amd64|_linux_amd64|"
    "linux/arm64|_linux_arm64|"
    "linux/arm:7|_linux_armv7|"
    "darwin/amd64|_darwin_amd64|"
    "darwin/arm64|_darwin_arm64|"
    "windows/amd64|_windows_amd64|.exe"
    "windows/arm64|_windows_arm64|.exe"
)

# 过滤平台
if [[ -n "$PLATFORM_FILTER" ]]; then
    IFS=',' read -ra FILTER <<< "$PLATFORM_FILTER"
    FILTERED=()
    for platform in "${ALL_PLATFORMS[@]}"; do
        GOOS_GOARCH="${platform%%|*}"
        GOOS="${GOOS_GOARCH%%/*}"
        GOARCH_GOARM="${GOOS_GOARCH#*/}"
        GOARCH="${GOARCH_GOARM%:*}"
        for f in "${FILTER[@]}"; do
            f_trimmed="${f## }"
            f_trimmed="${f_trimmed%% }"
            if [[ "$GOOS/$GOARCH" == "$f_trimmed" ]]; then
                FILTERED+=("$platform")
                break
            fi
        done
    done
    ALL_PLATFORMS=("${FILTERED[@]}")
    if [[ ${#ALL_PLATFORMS[@]} -eq 0 ]]; then
        echo -e "${RED}错误: 未匹配任何平台${NC}"
        echo "可用: linux/amd64, linux/arm64, linux/armv7, darwin/amd64, darwin/arm64, windows/amd64, windows/arm64"
        exit 1
    fi
fi

# ─── 编译标志 ──────────────────────────────────────────
LDFLAGS="-s -w"
BUILD_TIME=$(date -u +"%Y-%m-%d %H:%M:%S UTC")
CGO_ENABLED=0

echo "目标平台: ${#ALL_PLATFORMS[@]} 个"
echo "LDFLAGS: $LDFLAGS"
echo "CGO_ENABLED: $CGO_ENABLED"
echo ""

# 创建输出目录
mkdir -p "$OUTPUT_DIR"

# ─── 逐平台编译 ────────────────────────────────────────
SUCCESS_COUNT=0
FAIL_COUNT=0
declare -a BINARIES

for platform_def in "${ALL_PLATFORMS[@]}"; do
    # 解析平台定义: "linux/arm:7|_linux_armv7|" -> GOOS=linux, GOARCH=arm, GOARM=7, SUFFIX=_linux_armv7, EXT=""
    IFS='|' read -r GOOS_GOARCH SUFFIX EXT <<< "$platform_def"
    GOOS="${GOOS_GOARCH%%/*}"
    GOARCH_GOARM="${GOOS_GOARCH#*/}"
    if [[ "$GOARCH_GOARM" == *:* ]]; then
        GOARCH="${GOARCH_GOARM%:*}"
        GOARM="${GOARCH_GOARM#*:}"
    else
        GOARCH="$GOARCH_GOARM"
        GOARM=""
    fi

    BINARY_NAME="remotebash${SUFFIX}${EXT}"
    OUTPUT_PATH="${OUTPUT_DIR}/${BINARY_NAME}"

    LABEL="$GOOS/$GOARCH"
    [[ -n "$GOARM" ]] && LABEL="$LABEL (GOARM=$GOARM)"

    echo -e "${YELLOW}[编译]${NC} $LABEL  -->  $BINARY_NAME"

    export GOOS="$GOOS"
    export GOARCH="$GOARCH"
    export CGO_ENABLED="$CGO_ENABLED"
    [[ -n "$GOARM" ]] && export GOARM="$GOARM" || unset GOARM

    if go build -ldflags="$LDFLAGS" -o "$OUTPUT_PATH" ./cmd/remotebash/ 2>&1; then
        if command -v stat &>/dev/null; then
            # macOS 的 stat 语法不同
            if [[ "$(uname)" == "Darwin" ]]; then
                SIZE_BYTES=$(stat -f%z "$OUTPUT_PATH")
            else
                SIZE_BYTES=$(stat -c%s "$OUTPUT_PATH")
            fi
        else
            SIZE_BYTES=$(wc -c < "$OUTPUT_PATH" | tr -d ' ')
        fi
        SIZE_KB=$(echo "scale=1; $SIZE_BYTES / 1024" | bc 2>/dev/null || awk "BEGIN {printf \"%.1f\", $SIZE_BYTES / 1024}")
        echo -e "  ${GREEN}✓ 成功${NC}  (${SIZE_KB} KB)"

        SUCCESS_COUNT=$((SUCCESS_COUNT + 1))
        BINARIES+=("$BINARY_NAME|$OUTPUT_PATH|$LABEL|$SIZE_BYTES")
    else
        echo -e "  ${RED}✗ 失败${NC}"
        FAIL_COUNT=$((FAIL_COUNT + 1))
    fi
done

# ─── 生成校验文件 ──────────────────────────────────────
if [[ $SUCCESS_COUNT -gt 0 ]]; then
    echo ""
    echo -e "${CYAN}=== 生成 SHA256 校验 ===${NC}"

    CHECKSUM_FILE="${OUTPUT_DIR}/checksums.txt"
    {
        echo "# RemoteBash $VERSION — SHA256 Checksums"
        echo "# Generated: $BUILD_TIME"
        echo "# "
        echo "# Verify:  sha256sum -c checksums.txt          (Linux/macOS)"
        echo "#          certutil -hashfile <file> SHA256    (Windows)"
        echo "#"
    } > "$CHECKSUM_FILE"

    for entry in "${BINARIES[@]}"; do
        IFS='|' read -r BIN_NAME BIN_PATH BIN_PLATFORM BIN_SIZE <<< "$entry"
        HASH=$(sha256sum "$BIN_PATH" | awk '{print $1}')
        echo "$HASH  $BIN_NAME" >> "$CHECKSUM_FILE"
    done

    echo -e "  ${GREEN}✓${NC} $CHECKSUM_FILE"
fi

# ─── 汇总 ─────────────────────────────────────────────
echo ""
echo -e "${CYAN}=== 编译汇总 ===${NC}"
if [[ $FAIL_COUNT -eq 0 ]]; then
    echo -e "成功: ${GREEN}$SUCCESS_COUNT${NC}  失败: $FAIL_COUNT"
else
    echo -e "成功: $SUCCESS_COUNT  失败: ${RED}$FAIL_COUNT${NC}"
fi
echo "输出目录: $(cd "$OUTPUT_DIR" && pwd)"

echo ""
echo "产物列表:"
for entry in "${BINARIES[@]}"; do
    IFS='|' read -r BIN_NAME BIN_PATH BIN_PLATFORM BIN_SIZE <<< "$entry"
    SIZE_KB=$(echo "scale=1; $BIN_SIZE / 1024" | bc 2>/dev/null || awk "BEGIN {printf \"%.1f\", $BIN_SIZE / 1024}")
    echo "  $BIN_NAME  (${SIZE_KB} KB)  [$BIN_PLATFORM]"
done

[[ $FAIL_COUNT -gt 0 ]] && exit 1 || exit 0
