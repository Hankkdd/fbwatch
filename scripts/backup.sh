#!/bin/sh
# 每天把資料庫備份一份到宿主機目錄，保留最近 N 份。
#
#   backup.sh         常駐：每天 FBWATCH_BACKUP_AT 跑一次（compose 的 backup 服務用這個）
#   backup.sh once    立刻備份一份就結束 —— 動正式庫之前先跑這個
#
# 為什麼要有這支：2026-11-24 對正式庫跑了測試，groups / keywords / shops / listings
# 全被清掉。設定靠 pageinspect 從死亡 tuple 挖回來，listings 已經被 autovacuum
# 回收，兩個月 1,666 筆貼文永久遺失。那時候這個庫一份備份都沒有。
#
# 刻意的設計：
#   先寫 .tmp、用 pg_restore --list 讀過一遍才改名。寫到一半斷掉（磁碟滿、容器被停）
#   的檔案不會被當成備份，也就不會在輪替時把一份好的擠掉。
#   輪替只在成功之後做。連續失敗的時候舊備份全部留著，不會越備越少。
#   失敗要送 Discord。備份壞掉是安靜的：沒人去看就不會知道，
#   等到真的要還原那天才發現最後一份是三個月前的。
set -eu

DIR=/backups
KEEP="${FBWATCH_BACKUP_KEEP:-14}"
AT="${FBWATCH_BACKUP_AT:-04:00}"
DB="${PGDATABASE:-fbwatch}"

log() { echo "$(date '+%Y/%m/%d %H:%M:%S') $*"; }

# 送不出去也不能讓備份流程跟著死，所以一律 || true。
notify() {
	[ -n "${DISCORD_WEBHOOK_URL:-}" ] || return 0
	msg=$(printf '%s' "$1" | sed 's/\\/\\\\/g; s/"/\\"/g')
	wget -q -O /dev/null --header 'Content-Type: application/json' \
		--post-data "{\"content\":\"$msg\"}" "$DISCORD_WEBHOOK_URL" 2>/dev/null || true
}

prune() {
	# 檔名帶時間戳，字典序就是時間序。
	ls -1 "$DIR"/"$DB"-*.dump 2>/dev/null | sort -r | tail -n +"$((KEEP + 1))" |
		while read -r old; do
			rm -f -- "$old"
			log "輪替刪除 $(basename "$old")"
		done
}

backup() {
	ts=$(date +%Y%m%d-%H%M%S)
	out="$DIR/$DB-$ts.dump"
	tmp="$DIR/.$DB-$ts.dump.tmp"
	rm -f "$DIR"/."$DB"-*.dump.tmp # 上次中斷留下的

	# -Fc：壓縮過，而且可以只還原其中幾張表 —— 今天那種「只壞了三張表」的狀況用得到。
	if ! err=$(pg_dump -Fc -f "$tmp" "$DB" 2>&1); then
		rm -f "$tmp"
		log "備份失敗: $err"
		notify "💾 資料庫備份失敗，舊備份都還在、沒有輪替。$(echo "$err" | head -1)"
		return 1
	fi
	if ! pg_restore --list "$tmp" >/dev/null 2>&1; then
		rm -f "$tmp"
		log "備份檔讀不回來，丟棄"
		notify "💾 資料庫備份寫出來了但讀不回來，已丟棄。舊備份都還在、沒有輪替。"
		return 1
	fi
	mv "$tmp" "$out"
	log "備份完成 $(basename "$out")（$(du -h "$out" | cut -f1)）"
	prune
}

# 下一個 AT 還要幾秒。台灣沒有日光節約時間，一天固定 86400 秒。
seconds_until_next() {
	now=$(date +%s)
	target=$(date -d "$(date +%Y-%m-%d) $AT" +%s)
	[ "$target" -gt "$now" ] || target=$((target + 86400))
	echo $((target - now))
}

if [ "${1:-}" = once ]; then
	backup
	exit
fi

case "$KEEP" in '' | *[!0-9]* | 0)
	log "FBWATCH_BACKUP_KEEP 必須是正整數，實得 '$KEEP'"
	exit 1
	;;
esac
date -d "2000-01-01 $AT" >/dev/null 2>&1 || {
	log "FBWATCH_BACKUP_AT 必須是 HH:MM，實得 '$AT'"
	exit 1
}

log "備份排程：每天 $AT，保留 $KEEP 份，目錄 $DIR"

# 啟動時最近一天內沒有備份就先補一份：剛部署完立刻就有，
# 機器剛好在 AT 那個時間點關著也不會整天沒備。
if [ -z "$(find "$DIR" -maxdepth 1 -name "$DB-*.dump" -mmin -1440 2>/dev/null)" ]; then
	log "最近 24 小時內沒有備份，先補一份"
	backup || true
fi

while :; do
	s=$(seconds_until_next)
	log "下次備份 $((s / 3600)) 小時 $((s % 3600 / 60)) 分後"
	sleep "$s"
	backup || true
done
