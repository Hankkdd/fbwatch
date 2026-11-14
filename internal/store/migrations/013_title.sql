-- 商品卡片上的標題。
--
-- 很多賣家內文只寫「售如標題」，品名只在標題裡，不存下來的話通知和比價都看不到在賣什麼。
-- 舊資料用 cmd/backfilltitle 從 raw_html 回填。
ALTER TABLE listings ADD COLUMN IF NOT EXISTS title TEXT;
