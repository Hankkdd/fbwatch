#!/usr/bin/env bash
# 啟動瀏覽器工作階段（在 GX10 上執行）。
#
# 組成：Xvfb :99 → openbox → x11vnc(localhost) → websockify/noVNC(Tailscale)
# 存取方式：從自己的電腦用瀏覽器開 http://<tailscale-ip>:6080/vnc.html
#
# Chrome 由這支腳本手動啟動，go-rod 之後用 CDP 附掛。
# 不經過 automation launcher，因此不帶 --enable-automation，navigator.webdriver 維持 false。
#
# 解析度與 profile 路徑一旦決定就不要改 —— 兩者都是指紋組成，變動會觸發 FB 的新裝置檢查。
set -euo pipefail

DISPLAY_NUM="${DISPLAY_NUM:-:99}"
RES="1920x1080x24"
WIN_SIZE="1920,1080"
PROFILE_DIR="${PROFILE_DIR:-$HOME/fb-group-watch/chrome-profile}"
DEBUG_PORT="${DEBUG_PORT:-9222}"
VNC_PORT="${VNC_PORT:-5999}"
WEB_PORT="${WEB_PORT:-6080}"
VNC_PASSWD="${VNC_PASSWD:-$HOME/.vnc/passwd}"
NOVNC_ROOT="${NOVNC_ROOT:-/usr/share/novnc}"

running() { pgrep -f "$1" >/dev/null 2>&1; }

# --- Xvfb ---
if ! running "Xvfb ${DISPLAY_NUM}"; then
  echo "starting Xvfb on ${DISPLAY_NUM} (${RES})"
  Xvfb "${DISPLAY_NUM}" -screen 0 "${RES}" -nolisten tcp >/dev/null 2>&1 &
  sleep 1
else
  echo "Xvfb ${DISPLAY_NUM} already running"
fi

# --- window manager ---
# 沒有 WM 的話 Chrome 視窗拿不到鍵盤焦點，VNC 進去會打不了字。
if ! DISPLAY="${DISPLAY_NUM}" xprop -root _NET_SUPPORTING_WM_CHECK >/dev/null 2>&1; then
  if command -v openbox >/dev/null 2>&1; then
    echo "starting openbox on ${DISPLAY_NUM}"
    DISPLAY="${DISPLAY_NUM}" openbox >/dev/null 2>&1 &
    sleep 1
  else
    echo "WARNING: openbox 未安裝，VNC 內無法使用鍵盤 (sudo apt install -y openbox)" >&2
  fi
else
  echo "window manager already running on ${DISPLAY_NUM}"
fi

# --- x11vnc (只綁 localhost，對外由 websockify 代理) ---
if ! running "x11vnc.*${DISPLAY_NUM}"; then
  auth_args=(-nopw)
  if [ -f "${VNC_PASSWD}" ]; then
    auth_args=(-rfbauth "${VNC_PASSWD}")
  else
    echo "WARNING: 找不到 ${VNC_PASSWD}，VNC 將無密碼 (執行 x11vnc -storepasswd 設定)" >&2
  fi
  echo "starting x11vnc on ${VNC_PORT} (localhost only)"
  x11vnc -display "${DISPLAY_NUM}" -rfbport "${VNC_PORT}" -localhost \
         "${auth_args[@]}" -forever -quiet -bg >/dev/null 2>&1
else
  echo "x11vnc already running"
fi

# --- websockify / noVNC (綁 Tailscale 介面) ---
TS_IP="$(tailscale ip -4 2>/dev/null | head -1 || true)"
if [ -z "${TS_IP}" ]; then
  echo "WARNING: 取不到 Tailscale IP，改綁 localhost" >&2
  TS_IP="127.0.0.1"
fi
if ! running "websockify.*${WEB_PORT}"; then
  if [ -d "${NOVNC_ROOT}" ]; then
    echo "starting websockify on ${TS_IP}:${WEB_PORT}"
    websockify --web="${NOVNC_ROOT}" "${TS_IP}:${WEB_PORT}" "localhost:${VNC_PORT}" \
      >/dev/null 2>&1 &
    sleep 1
  else
    echo "WARNING: 找不到 ${NOVNC_ROOT} (sudo apt install -y novnc websockify)" >&2
  fi
else
  echo "websockify already running"
fi

# --- Chrome ---
if ! running "user-data-dir=${PROFILE_DIR}"; then
  echo "starting Chrome (profile: ${PROFILE_DIR}, cdp: ${DEBUG_PORT})"
  mkdir -p "${PROFILE_DIR}"
  chmod 700 "${PROFILE_DIR}"
  DISPLAY="${DISPLAY_NUM}" TZ="Asia/Taipei" google-chrome \
    --user-data-dir="${PROFILE_DIR}" \
    --remote-debugging-port="${DEBUG_PORT}" \
    --window-size="${WIN_SIZE}" \
    --window-position=0,0 \
    --lang=zh-TW \
    --no-first-run \
    --no-default-browser-check \
    --disable-session-crashed-bubble \
    about:blank >/dev/null 2>&1 &
  sleep 3
else
  echo "Chrome already running on this profile"
fi

echo
echo "noVNC:  http://${TS_IP}:${WEB_PORT}/vnc.html   (從自己的電腦用瀏覽器開)"
echo "CDP:    http://127.0.0.1:${DEBUG_PORT}          (GX10 本機)"
