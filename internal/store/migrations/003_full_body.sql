-- 詳情頁的內嵌 JSON 帶來的欄位。
-- feed 上的內文是伺服器端截斷的，詳情頁的 DOM 也是 —— 但它的內嵌 JSON 是完整的，
-- 所以取完整內文不需要任何點擊或互動。
ALTER TABLE listings ADD COLUMN IF NOT EXISTS location        TEXT;
ALTER TABLE listings ADD COLUMN IF NOT EXISTS currency        TEXT;

-- posted_at 可能來自兩處：feed 的相對時間推算（小時級，實測誤差達 15 分鐘）
-- 或詳情頁的 creation_time（精確）。量測延遲時要知道用的是哪一個。
ALTER TABLE listings ADD COLUMN IF NOT EXISTS posted_at_exact BOOLEAN NOT NULL DEFAULT FALSE;

-- 抓取次數上限，避免對同一則反覆請求
ALTER TABLE listings ADD COLUMN IF NOT EXISTS body_attempts   INTEGER NOT NULL DEFAULT 0;

-- 必須先 DROP。CREATE OR REPLACE VIEW 只能在尾端追加欄位，
-- 在中間插入會失敗（cannot change name of view column），
-- 而 migration 每次啟動都會重跑，失敗就變成容器重啟迴圈。
DROP VIEW IF EXISTS notify_latency;
CREATE VIEW notify_latency AS
SELECT
    id,
    group_id,
    status,
    price,
    posted_at,
    posted_at_exact,
    first_seen,
    notified_at,
    first_seen  - posted_at  AS feed_lag,
    notified_at - first_seen AS notice_lag,
    notified_at - posted_at  AS total_lag
FROM listings
WHERE posted_at IS NOT NULL AND notified_at IS NOT NULL;
