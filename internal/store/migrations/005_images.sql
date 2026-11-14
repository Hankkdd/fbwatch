-- 圖片以內容雜湊去重，實際位元組存在 MinIO，這裡只記中繼資料。
--
-- 拆兩張表是因為多對多：跨社團轉貼佔實測資料的 39%，
-- 同一張圖會被多則 listing 引用，只該存一份。
CREATE TABLE IF NOT EXISTS images (
    sha256       TEXT PRIMARY KEY,
    object_key   TEXT        NOT NULL,   -- MinIO 的 key，雜湊加副檔名
    bytes        INTEGER     NOT NULL,
    content_type TEXT,
    stored_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS listing_images (
    listing_id TEXT    NOT NULL REFERENCES listings(id) ON DELETE CASCADE,
    idx        INTEGER NOT NULL,          -- 貼文內的順序，要保留
    photo_id   TEXT,                      -- FB 的照片 id，可在下載前就去重
    sha256     TEXT REFERENCES images(sha256),
    cdn_url    TEXT,                      -- 帶簽章會過期，僅備查
    caption    TEXT,                      -- FB 自動生成的描述，含辨識出的文字
    PRIMARY KEY (listing_id, idx)
);

CREATE INDEX IF NOT EXISTS listing_images_photo ON listing_images (photo_id) WHERE photo_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS listing_images_sha   ON listing_images (sha256);

ALTER TABLE listings ADD COLUMN IF NOT EXISTS images_fetched BOOLEAN NOT NULL DEFAULT FALSE;
