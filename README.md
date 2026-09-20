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
| 2b | 完整內文抓取 | 未開始 |
| 2c | 容器化 + Postgres | 完成 |
| 3 | 排程輪詢 + 熔斷 | 部分（排程完成，熔斷未做） |
| 4 | Discord 通知 | 部分（只送連結） |

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

store 的測試需要資料庫，未設 `FBWATCH_TEST_DSN` 會自動跳過：

```bash
FBWATCH_TEST_DSN='postgres://fbwatch:<pw>@localhost:55432/fbwatch?sslmode=disable' go test ./internal/store/
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
