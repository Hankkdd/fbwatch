-- 每個社團可以通知到不同的聊天室。
--
-- 存的是環境變數的「名字」而不是 webhook 本身：這個專案的憑證一律放 .env
-- （PG_PASSWORD、VNC_PASSWORD、MINIO_PASSWORD、DISCORD_WEBHOOK_URL 都在那裡），
-- 寫進資料庫的話 pg_dump 出來的檔案就帶著一把可以對那個頻道發文的鑰匙。
-- NULL 表示用預設的 DISCORD_WEBHOOK_URL。
ALTER TABLE groups ADD COLUMN IF NOT EXISTS webhook_env TEXT;
