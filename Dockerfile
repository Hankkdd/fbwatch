# syntax=docker/dockerfile:1.7

FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -o /out/collect ./cmd/collect \
 && CGO_ENABLED=0 go build -o /out/dumpdom ./cmd/dumpdom \
 && CGO_ENABLED=0 go build -o /out/analyze ./cmd/analyze


FROM debian:bookworm-slim

# 預設的 docker-clean 會在每次 apt 後刪掉 .deb，讓快取掛載失去意義
RUN rm -f /etc/apt/apt.conf.d/docker-clean \
 && echo 'Binary::apt::APT::Keep-Downloaded-Packages "true";' > /etc/apt/apt.conf.d/keep-cache

# 層級由「很少變動」往「常變動」排。改動下面的層不會觸發上面重新下載。

# 桌面工作階段與字型
RUN --mount=type=cache,target=/var/cache/apt,sharing=locked \
    --mount=type=cache,target=/var/lib/apt,sharing=locked \
    apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates curl gnupg locales tzdata \
      xvfb openbox x11vnc novnc websockify \
      fonts-noto-cjk

# Chrome 單獨一層：它是最大的一塊（安裝後 427MB），不該被其他套件的變動拖著重來。
# 自 2026-07-30 起有官方 arm64 Linux 建置，走同一個 apt repo。
# 不要用 snap 版 Chromium —— 沙箱封裝會讓 --user-data-dir 行為異常。
RUN --mount=type=cache,target=/var/cache/apt,sharing=locked \
    --mount=type=cache,target=/var/lib/apt,sharing=locked \
    curl -fsSL https://dl.google.com/linux/linux_signing_key.pub \
      | gpg --dearmor -o /usr/share/keyrings/google-chrome.gpg \
 && echo "deb [arch=$(dpkg --print-architecture) signed-by=/usr/share/keyrings/google-chrome.gpg] https://dl.google.com/linux/chrome/deb/ stable main" \
      > /etc/apt/sources.list.d/google-chrome.list \
 && apt-get update && apt-get install -y --no-install-recommends google-chrome-stable

RUN sed -i 's/^# *\(zh_TW.UTF-8\)/\1/' /etc/locale.gen && locale-gen
ENV TZ=Asia/Taipei LANG=zh_TW.UTF-8 LC_ALL=zh_TW.UTF-8

# 以非 root 執行，才不必加 --no-sandbox。
# UID 必須與宿主機 chrome-profile 目錄的擁有者相同，否則 Chrome 寫不進 profile。
ARG UID=1002
RUN useradd -m -u ${UID} -s /bin/bash fbwatch \
 && mkdir -p /tmp/.X11-unix && chmod 1777 /tmp/.X11-unix

COPY scripts/container-entrypoint.sh /app/entrypoint.sh
RUN chmod +x /app/entrypoint.sh
COPY --from=build /out/ /app/

USER fbwatch
WORKDIR /home/fbwatch
EXPOSE 6080
ENTRYPOINT ["/app/entrypoint.sh"]
