# ZYZU — 资源组 TG频道机器人

自动抓取 [ziyuanzu.com](https://www.ziyuanzu.com) 77个影视资源站采集接口，定时推送到Telegram频道。

## 功能

- 定时采集77个资源站（可配cron）
- 自动推送新站/更新到TG频道
- Web控制台（站列表、搜索、手动推送、黑名单）
- WebSocket实时状态
- Docker一键部署

## 快速开始

```bash
cp config.json.example config.json
# 编辑 config.json 填入 bot_token + channel_id

# Docker
docker compose up -d

# 或直接运行
go run ./cmd/zyzu/
```

访问 `http://localhost:8080` 打开控制台。

## 配置

```json
{
  "bot_token": "123456:ABC...",
  "channel_id": -1001234567890,
  "scrape_cron": "0 */6 * * *",
  "listen_addr": ":8080",
  "post_format": "📡 *{name}*  |  {availability}  |  {resource_count}条"
}
```

## 构建

```bash
make build        # Windows
make build-linux  # Linux (CGO_ENABLED=0)
make docker       # Docker镜像
```

## API

| 端点 | 说明 |
|------|------|
| GET /api/stations | 所有站点 |
| POST /api/stations/{slug}/blacklist | 屏蔽/解除 |
| POST /api/stations/{slug}/post | 手动推送 |
| POST /api/trigger | 立即采集 |
| GET/PUT /api/config | 配置读写 |
| GET /api/status | 调度状态 |
| GET /api/history | 推送历史 |

## 技术栈

Go + chi + pure-Go SQLite + vanilla JS Web UI + Docker
