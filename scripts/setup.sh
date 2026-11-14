#!/usr/bin/env bash
#
# 產生 .env 與 config/seed.yaml，問完該問的就收工。
#
# 不做安裝也不啟動容器 —— docker compose 已經是安裝器了，這支只負責
# 把人會填錯的那幾格填對：UID、四組密碼、webhook 網址、社團 id。
set -euo pipefail

cd "$(dirname "$0")/.."

say()  { printf '%s\n' "$*"; }
warn() { printf '\033[33m%s\033[0m\n' "$*" >&2; }
die()  { printf '\033[31m%s\033[0m\n' "$*" >&2; exit 1; }

# ── 先確認工具齊全。缺東西要在問問題之前就講，不要讓人填完一輪才失敗 ──
for cmd in docker openssl; do
    command -v "$cmd" >/dev/null || die "找不到 $cmd，請先安裝。"
done
docker compose version >/dev/null 2>&1 || die "docker compose v2 不可用。舊版的 docker-compose 不支援這裡用到的 profile。"

# 既有設定一律不覆蓋：裡面是已經在用的密碼，蓋掉等於把資料庫鎖在外面。
[ -e .env ] && die ".env 已經存在。要重新產生請先自己備份並刪除。"
[ -e config/seed.yaml ] && die "config/seed.yaml 已經存在。要重新產生請先自己備份並刪除。"
[ -f .env.example ] || die "找不到 .env.example，請在專案根目錄執行。"

say "fbwatch 設定"
say "============"
say ""

# ── UID ──
# 容器裡的 Chrome 要寫 chrome-profile/。UID 不一致時它寫不進去，
# 症狀是「每次重啟都要重新登入」，而那離成因非常遠，所以這裡直接帶。
HOST_UID="$(id -u)"
say "容器 UID：$HOST_UID（取自 id -u）"

# ── Discord webhook ──
say ""
say "Discord webhook：在頻道設定 → 整合 → Webhook → 複製 Webhook 網址"
while :; do
    read -r -p "webhook 網址：" WEBHOOK
    case "$WEBHOOK" in
        https://discord.com/api/webhooks/*|https://discordapp.com/api/webhooks/*|https://ptb.discord.com/api/webhooks/*)
            break ;;
        "") warn "不能留空 —— 沒有 webhook 就收不到任何通知。" ;;
        *)  warn "看起來不像 webhook 網址，應該以 https://discord.com/api/webhooks/ 開頭。" ;;
    esac
done

# ── 社團 ──
say ""
say "要監控的社團。貼網址或純數字 id 都可以，一行一個，空白行結束。"
say "（用來登入的那個帳號必須已經加入該社團）"
# 不要叫 GROUPS：那是 bash 的內建變數（使用者的 unix 群組）。
# 賦值會被忽略，而且在 set -e 下會讓整個迴圈第一圈就結束。
group_ids=()
while :; do
    read -r -p "社團：" g || break
    [ -z "$g" ] && break
    # 從網址抽出 id；本來就是純數字就原樣留著
    gid="$(printf '%s' "$g" | sed -n 's#.*/groups/\([0-9][0-9]*\).*#\1#p')"
    [ -z "$gid" ] && gid="$(printf '%s' "$g" | sed -n 's#^[^0-9]*\([0-9][0-9]*\)[^0-9]*$#\1#p')"
    if [ -z "$gid" ]; then
        warn "認不出社團 id：$g"
        continue
    fi
    group_ids+=("$gid")
    say "  → $gid"
done
[ ${#group_ids[@]} -eq 0 ] && warn "沒有填社團。之後可以自己編輯 config/seed.yaml。"

# ── 綁定位址 ──
say ""
say "noVNC 要綁在哪個位址？直接 Enter 用 127.0.0.1（只有本機連得到）。"
say "要從別台機器連就填那張網卡的位址，但請走 Tailscale 之類的私有網路 ——"
say "noVNC 後面是一個已經登入你 Facebook 的瀏覽器。"
while :; do
    read -r -p "BIND_IP [127.0.0.1]：" BIND_IP
    BIND_IP="${BIND_IP:-127.0.0.1}"
    case "$BIND_IP" in
        *[!0-9.]*|""|*..*) warn "要填 IPv4 位址，例如 127.0.0.1。" ;;
        *) break ;;
    esac
done
[ "$BIND_IP" = "0.0.0.0" ] && warn "綁 0.0.0.0 等於把你的 Facebook 帳號放上公網。真的確定再這樣用。"

# ── 寫檔 ──
# 從範本改而不是重新生成：範本裡的註解才是真正在解釋每個變數的東西。
gen() { openssl rand -hex 32; }
sed -e "s|^HOST_UID=.*|HOST_UID=$HOST_UID|" \
    -e "s|^PG_PASSWORD=.*|PG_PASSWORD=$(gen)|" \
    -e "s|^VNC_PASSWORD=.*|VNC_PASSWORD=$(gen)|" \
    -e "s|^MINIO_PASSWORD=.*|MINIO_PASSWORD=$(gen)|" \
    -e "s|^BIND_IP=.*|BIND_IP=$BIND_IP|" \
    -e "s|^DISCORD_WEBHOOK_URL=.*|DISCORD_WEBHOOK_URL=$WEBHOOK|" \
    .env.example > .env
chmod 600 .env

{
    say "# 由 scripts/setup.sh 產生。只在對應的資料表是空的時候套用，"
    say "# 之後以資料庫為準。可追加的東西見 config/seed.example.yaml。"
    say ""
    if [ ${#group_ids[@]} -gt 0 ]; then
        say "groups:"
        for gid in "${group_ids[@]}"; do
            say "  - id: \"$gid\""
        done
    else
        say "groups: []"
    fi
} > config/seed.yaml

# 確認產出的東西 compose 讀得進去，不要讓人到下一步才發現。
# 不過關就把剛寫的檔案收掉 —— 留著半套設定，下次再跑會被「已經存在」擋住，
# 而那個檔案其實是壞的。
if ! docker compose config -q; then
    rm -f .env config/seed.yaml
    die "產生的設定 compose 讀不過，已經把它們刪掉了。請看上面的錯誤訊息。"
fi

say ""
say "完成："
say "  .env              密碼已產生，權限 600"
say "  config/seed.yaml  ${#group_ids[@]} 個社團"
say ""
say "接下來："
say "  1. docker compose up -d --build"
say "  2. 開 http://$BIND_IP:6080/vnc.html，在裡面的 Chrome 登入 Facebook"
say "  3. 登入好之後把 .env 的 FBWATCH_LOOP 改成 1，再 docker compose up -d"
say ""
say "關鍵字提示、主題過濾、賣貨便賣場等設定見 config/seed.example.yaml。"
