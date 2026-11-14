#!/usr/bin/env bash
# collector 容器的入口：把瀏覽器工作階段拉起來，然後常駐。
#
# 抓取本身由 `docker compose exec collector /app/collect` 觸發（Stage 2c），
# Stage 3 會換成常駐 daemon。
#
# 解析度與 profile 路徑不要改 —— 兩者都是指紋組成。
set -euo pipefail

DISPLAY_NUM=":99"
RES="1920x1080x24"
WIN_SIZE="1920,1080"
PROFILE_DIR="/profile"
DEBUG_PORT="9222"
VNC_PORT="5999"
WEB_PORT="6080"

mkdir -p "${PROFILE_DIR}"

N="${DISPLAY_NUM#:}"

# Docker 重啟容器沿用同一個可寫層，/tmp 不會被清空。
# 上一輪留下的 X lock 會讓 Xvfb 永遠起不來，變成無限重啟迴圈 ——
# 原本只是 Chrome 崩潰，卻會惡化成整個工作階段再也拉不起來。
rm -f "/tmp/.X${N}-lock" "/tmp/.X11-unix/X${N}"

# Chrome 上次非正常結束會留下鎖，導致 profile 開不起來
rm -f "${PROFILE_DIR}/SingletonLock" "${PROFILE_DIR}/SingletonCookie" "${PROFILE_DIR}/SingletonSocket"

echo "starting Xvfb ${DISPLAY_NUM} (${RES})"
Xvfb "${DISPLAY_NUM}" -screen 0 "${RES}" -nolisten tcp &

# 明確等待 socket 出現，不要用 sleep 猜。
# Xvfb 起不來就直接失敗，否則後面每個元件都會噴一大串誤導性的錯誤。
for _ in $(seq 40); do
  [ -S "/tmp/.X11-unix/X${N}" ] && break
  sleep 0.25
done
if [ ! -S "/tmp/.X11-unix/X${N}" ]; then
  echo "FATAL: Xvfb 未能在 ${DISPLAY_NUM} 上啟動" >&2
  exit 1
fi

# 沒有 WM 的話 Chrome 視窗拿不到鍵盤焦點，登入時會打不了字
echo "starting openbox"
DISPLAY="${DISPLAY_NUM}" openbox &
sleep 1

auth=(-nopw)
if [ -n "${VNC_PASSWORD:-}" ]; then
  mkdir -p "$HOME/.vnc"
  x11vnc -storepasswd "${VNC_PASSWORD}" "$HOME/.vnc/passwd" >/dev/null 2>&1
  auth=(-rfbauth "$HOME/.vnc/passwd")
else
  echo "WARNING: 未設 VNC_PASSWORD，noVNC 將無密碼" >&2
fi

echo "starting x11vnc"
x11vnc -display "${DISPLAY_NUM}" -rfbport "${VNC_PORT}" -localhost \
       "${auth[@]}" -forever -quiet -bg

echo "starting noVNC on ${WEB_PORT}"
websockify --web=/usr/share/novnc "0.0.0.0:${WEB_PORT}" "localhost:${VNC_PORT}" &

echo "starting Chrome"
DISPLAY="${DISPLAY_NUM}" google-chrome \
  --user-data-dir="${PROFILE_DIR}" \
  --remote-debugging-port="${DEBUG_PORT}" \
  --window-size="${WIN_SIZE}" \
  --window-position=0,0 \
  --lang=zh-TW \
  --no-first-run \
  --no-default-browser-check \
  --disable-session-crashed-bubble \
  about:blank &

echo
echo "ready. 首次使用請開 noVNC 登入 Facebook。"

if [ "${FBWATCH_LOOP:-0}" = "1" ]; then
  echo "starting collect loop"
  sleep 5
  /app/collect -loop &
fi

wait -n
