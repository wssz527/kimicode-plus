#!/bin/bash
# 启动当前 Go 版本增强核心（独立管理页）。
# 接入官方客户端需要额外的隔离宿主参数，见 README「本机验证」一节。
set -euo pipefail
cd "$(dirname "$0")"
export PATH="/opt/homebrew/bin:/usr/local/bin:$PATH"

DATA_DIR="${EKD_DATA_DIR:-./work/development}"
PORT="${EKD_PORT:-0}"
LOG="$DATA_DIR/core.log"

if [ ! -x "./dist/enhanced-kimi" ]; then
  echo "未找到 ./dist/enhanced-kimi，先构建当前版本。"
  if ! command -v go >/dev/null 2>&1 && [ -x "${EKD_GO:-./work/toolchains/go/bin/go}" ]; then
    export PATH="$(cd "$(dirname "${EKD_GO:-./work/toolchains/go/bin/go}")" && pwd):$PATH"
  fi
  if ! command -v go >/dev/null 2>&1; then
    echo "缺少 Go 工具链：把 go 放进 PATH，或先手动执行 sh scripts/build.sh。"
    read -r -p "按回车键关闭窗口。" _
    exit 1
  fi
  sh scripts/build.sh
fi

mkdir -p "$DATA_DIR"
echo "数据目录：$DATA_DIR"
echo "日志：$LOG"
RUNTIME="$DATA_DIR/runtime.json"
# A previous run killed by SIGKILL or a power loss never reached its `defer
# os.Remove`, so a stale file would hand this script the old port and the old
# (already invalid) pairing token. Remove only this data directory's file before
# starting, so the wait loop below can only observe the new process's record.
if [ -e "$RUNTIME" ]; then
  echo "清理上次异常退出残留的 runtime.json"
  rm -f "$RUNTIME"
fi

"./dist/enhanced-kimi" --data-dir "$DATA_DIR" --port "$PORT" >>"$LOG" 2>&1 &
PID=$!
trap 'kill "$PID" 2>/dev/null || true' EXIT INT TERM

for _ in $(seq 1 100); do
  if [ -f "$RUNTIME" ]; then break; fi
  if ! kill -0 "$PID" 2>/dev/null; then
    echo "增强核心未能启动，最近日志："
    tail -n 20 "$LOG" || true
    read -r -p "按回车键关闭窗口。" _
    exit 1
  fi
  sleep 0.2
done

URL=$(sed -n 's/.*"pairingUrl": *"\([^"]*\)".*/\1/p' "$RUNTIME" | head -n 1)
if [ -z "$URL" ]; then
  echo "没有读到 runtime.json 中的配对地址，请查看 $LOG。"
else
  echo "配对地址：$URL"
  open "$URL" || echo "请手动在浏览器打开上面的地址完成配对。"
fi
echo "增强核心运行中；按 Ctrl+C 停止。"
wait "$PID"
