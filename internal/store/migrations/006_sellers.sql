-- 賣家。靜音的賣家照常存進資料庫，只是通知改成一行而不是整篇。
--
-- 刻意不「不通知」：完全不出聲會產生靜默盲區，看不出他到底還有沒有在發。
-- 一行的成本夠低，資訊也還在。
CREATE TABLE IF NOT EXISTS sellers (
    id         TEXT PRIMARY KEY,
    name       TEXT,
    muted      BOOLEAN     NOT NULL DEFAULT FALSE,
    note       TEXT,
    first_seen TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS sellers_muted ON sellers (muted) WHERE muted;

-- label 是你自己給的稱呼，優先於 FB 的顯示名稱。
-- 需要分開兩欄：name 每輪都會被 FB 的顯示名稱覆蓋，
-- 直接改 name 下一輪就會被蓋回去。
ALTER TABLE sellers ADD COLUMN IF NOT EXISTS label TEXT;
