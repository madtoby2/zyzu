# ZYZU — 资源组 TG频道机器人

> **[yunmatai.xyz](https://yunmatai.xyz)** — TG自动化工具集 · 云控 · 采集 · 营销

自动抓取 [ziyuanzu.com](https://www.ziyuanzu.com) 77个影视资源站，定时推送可播放视频到Telegram频道。

## 功能

- 定时采集77个资源站（可配cron）
- 三种推送模式：**video**(原生视频) / split(封面+m3u8) / digest(列表)
- Web控制台 — 站列表、搜索、手动推送、黑名单
- WebSocket实时状态
- Docker一键部署，自带ffmpeg

## 快速开始

```bash
git clone https://github.com/madtoby2/zyzu.git
cd zyzu
cp config.json.example config.json
# 编辑 config.json 填入 bot_token + channel_id

# Docker
docker compose up -d

# 或直接运行 (需装ffmpeg)
go run ./cmd/zyzu/
```

访问 `http://localhost:8080` 打开控制台。

## 配置

```json
{
  "bot_token": "123456:ABC...",
  "channel_id": -1001234567890,
  "scrape_cron": "0 */6 * * *",
  "content_cron": "0 8,20 * * *",
  "content_mode": "video",
  "content_limit": 5,
  "listen_addr": ":8080"
}
```

| 字段 | 说明 |
|------|------|
| content_mode | video(下载m3u8→mp4上传) / split(封面+链接) / digest(文本) |
| content_limit | 每次发几条 |
| content_cron | 内容更新频率 |

## API

| 端点 | 说明 |
|------|------|
| GET /api/stations | 所有站点 |
| POST /api/stations/{slug}/blacklist | 屏蔽/解除 |
| POST /api/stations/{slug}/post | 手动推送 |
| POST /api/trigger | 立即采集 |
| POST /api/content/trigger | 立即抓内容 |
| GET/PUT /api/config | 配置读写 |
| GET /api/status | 调度状态 |
| GET /api/history | 推送历史 |

## 技术栈

Go + chi + pure-Go SQLite + ffmpeg + vanilla JS + Docker

---

**[yunmatai.xyz](https://yunmatai.xyz)** — 更多TG自动化工具
