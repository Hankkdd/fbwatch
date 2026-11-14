-- 在意的主題。命中就在通知最上面多一行提示詞，原文一個字都不動。
--
-- 放資料表而不是寫死在程式裡，理由跟 groups、sellers 一樣：
-- 想追什麼、哪句話看膩了，都該能隨時改而不用重新部署。
--
-- patterns 是同義詞清單，建議從社團的歷史貼文挖，不要憑印象列。
-- 實測 464 則貼文：人憑印象列的講法跟社團實際在用的差很多 ——
--   正式名稱只出現 2 則，大家用的是英文原名與另一個俗稱
--   同一個詞還有詞序相反的寫法
--   最常見的講法是個暱稱，比正式名稱還多
--
-- 也要刻意排除「看起來像、實際不是」的詞：兩三個字母的縮寫尤其容易
-- 在別的名字裡撞到，挖的時候要把命中的貼文逐則看過再決定收不收。

CREATE TABLE IF NOT EXISTS keywords (
    label    TEXT PRIMARY KEY,
    line     TEXT   NOT NULL,              -- 要插在通知最上面的那句
    patterns TEXT[] NOT NULL,
    priority INTEGER NOT NULL DEFAULT 100, -- 小的排前面；命中多個時稀有的先講
    enabled  BOOLEAN NOT NULL DEFAULT TRUE
);
