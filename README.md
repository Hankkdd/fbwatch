# fbwatch

監控 Facebook 社團的拍賣貼文，新 listing 出現時把連結送到 Discord。

FB 對這類貼文沒有可用的通知管道——社團的「所有貼文」通知涵蓋不到帶價格的 listing，
Marketplace 的儲存搜尋通知在 2024 被移除，Groups API 的讀取端點也在同年關閉。
細節見 [SPEC.md](SPEC.md) §2。

## 現況

| Stage | 內容 | 狀態 |
|---|---|---|
| 1 | 瀏覽器工作階段、DOM 結構探勘 | 完成 |
| 2a | 解析器（容器錨點、內文、價格、狀態） | 完成 |
| 2b | 完整內文 + 精確時間 | 完成 |
| 2c | 容器化 + Postgres | 完成 |
| 3 | 排程輪詢 + 熔斷 | 完成 |
| 4 | Discord 通知 | 連結 + 完整內文，不過濾 |

尚未做：備用帳號切換、圖片保存、比價、Discord 問答。

通知一律送出，不依狀態過濾 —— 內文完整送達，判斷交給人。
`price` 與 `status` 的解析已知有誤判（中文數字如「一些」被當成價格、
交易條款裡的「售出」被當成已售出、目錄文只能存一個價格），但它們不影響
通知路徑，暫不處理。`body` 與 `raw_html` 都已保存，解析器改好後可回頭
重跑，`parser_version` 用來辨識哪些列需要重算。

## 圖片

商品照片存進 MinIO，以內容雜湊（sha256）為 key，中繼資料在 Postgres
的 `images` 與 `listing_images` 兩張表。拆兩張是因為多對多 ——
實測跨社團轉貼佔 39%，同一張圖會被多則 listing 引用，只該存一份。

用 MinIO 而非檔案系統，理由是管理性而非容量（估算約 20GB/年）：
content-addressed 的路徑用眼睛看不出是什麼，MinIO 的 console 可以
直接瀏覽與預覽。console 綁在 `BIND_IP` 上。

**圖片下載不需要登入態也不需要瀏覽器** —— 走 `scontent.*.fbcdn.net`，
純 HTTP GET 即可。所以這段對帳號風險幾乎為零：不同主機、不帶 session、
不增加 facebook.com 的請求。但網址帶簽章會過期，必須在抓到詳情頁的
當下就下載，不能只存網址。

兩種頁型的照片欄位不同，都要支援：

| 頁型 | 欄位 |
|---|---|
| 商品頁 | `listing_photos[].image` |
| 貼文頁 | `media.photo_image` 與 `all_subattachments.nodes[].media.image` |

只從這些已知欄位取，不是掃整份文件找 scontent 網址 —— 那會把大頭貼、
表情符號、介面圖示一起抓進來（實測誤抓過 80x80 的大頭貼），
另外再加尺寸下限 200px 把關。

已知限制：多圖貼文的初始 HTML 只帶一部分（實測 42 張的貼文拿到 9 張），
要全部得翻圖片檢視器，那是每張一次請求，不划算。

## 節奏調整

輪詢間隔與每輪補抓上限都可用環境變數調整（見 `.env.example`），
下限保護在 30 秒 —— 間隔太短會讓隨機化失去意義，變成穩定的高頻打點。

**調快的收益有限。** 實測 14 筆樣本的 feed_lag 最小值是 6:59，
且緊密集中在 7–11 分鐘。最小值卡在 7 分鐘、下面什麼都沒有，
代表那是 FB 端的地板（推測是商品審核或索引），不是輪詢抖動 ——
相位差只佔其中 1–2 分鐘。

## 熔斷

存在的理由只有一個：**重試迴圈會把軟性封鎖升級成硬封鎖。**

| 情況 | 行為 |
|---|---|
| 導向登入頁或 checkpoint | 立即停止，Discord 告警，不重試 |
| 連續 3 次抓取失敗 | 冷卻 30 分鐘後重試一次，再失敗則停止 |
| 6 小時沒有新項目 | Discord 提醒（解析器可能被改版打壞） |

熔斷後行程不會結束——容器設了 `restart`，結束會被重啟然後再打 FB 一次，
正是熔斷要避免的。改為停止輪詢但保持存活，noVNC 仍可連入重新登入。

## 架構

```
collector ──寫入──> Postgres <──讀取── notifier ──> Discord webhook
(Chrome + go-rod)   (唯一真實來源)
```

collector 只負責取得並寫入原始資料，不做通知、不做分析。爬蟲是最脆弱、
最不該頻繁改動的部分，加功能不該波及它。

## 啟動

```bash
cp .env.example .env   # 填入密碼、Discord webhook、社團 ID
docker compose up -d --build
```

首次使用要人工登入一次 Facebook：開 `http://<BIND_IP>:6080/vnc.html`。
登入態存在 `chrome-profile/`，之後重啟沿用。

手動跑一次抓取：

```bash
docker compose exec collector /app/collect
```

`FBWATCH_LOOP=1` 則容器啟動後自動持續輪詢。

## 開發工具

| 指令 | 用途 |
|---|---|
| `cmd/collect` | 抓取 → 解析 → 寫庫 → 通知 |
| `cmd/dumpdom` | 存下渲染後的 DOM 與截圖 |
| `cmd/analyze` | 對已存的 dump 跑解析器 |
| `cmd/probe` | FB 改版後重新定位 DOM 錨點 |

```bash
go test ./...
```

store 的測試需要資料庫，未設 `FBWATCH_TEST_DSN` 會自動跳過。

**要用獨立的資料庫，不要指向 `fbwatch`。** 測試會寫入 listing 列，
指向正式庫的話那些假資料會被當成新貼文推到 Discord。

```bash
docker compose exec postgres psql -U fbwatch -d postgres -c 'CREATE DATABASE fbwatch_test OWNER fbwatch;'
FBWATCH_TEST_DSN='postgres://fbwatch:<pw>@localhost:55432/fbwatch_test?sslmode=disable' go test ./internal/store/
```

## 不要提交的東西

`.gitignore` 已涵蓋，但值得知道原因：

- `chrome-profile/` —— 有效的 Facebook 登入態
- `.env` —— 資料庫密碼、VNC 密碼、Discord webhook
- `dumps/` —— 抓下來的貼文，含他人個資

## 文件

- [SPEC.md](SPEC.md) —— 技術設計與實測結果
- [REQUIREMENTS.md](REQUIREMENTS.md) —— 需求與不可逆決定

## 注意

抓取 Facebook 違反其服務條款。此專案僅供個人使用，風險自負。
