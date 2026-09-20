-- 貼文時間，由 FB 的相對時間字串（「25分鐘」「6小時」）換算而來。
-- 精度受限於 FB 自己的呈現：超過一小時只有小時級。
-- 剛發出的貼文是分鐘級，而那正是量測通知延遲需要的區間。
ALTER TABLE listings ADD COLUMN IF NOT EXISTS posted_at TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS listings_posted_at ON listings (posted_at DESC);

-- 通知延遲：貼文發出 → 我們送出通知。
-- notice_lag 是系統可控的部分（輪詢間隔），feed_lag 是 FB 自己的延遲。
CREATE OR REPLACE VIEW notify_latency AS
SELECT
    id,
    group_id,
    status,
    price,
    posted_at,
    first_seen,
    notified_at,
    first_seen  - posted_at  AS feed_lag,
    notified_at - first_seen AS notice_lag,
    notified_at - posted_at  AS total_lag
FROM listings
WHERE posted_at IS NOT NULL AND notified_at IS NOT NULL;
