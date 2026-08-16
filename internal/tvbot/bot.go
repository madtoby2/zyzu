package tvbot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/madtoby2/zyzu/internal/store"
)

type Bot struct {
	token          string
	category       string
	archiveChannel int64
	store          *store.Store
	client         *http.Client
	offset         int
}

type update struct {
	UpdateID      int            `json:"update_id"`
	Message       *message       `json:"message"`
	CallbackQuery *callbackQuery `json:"callback_query"`
}

type message struct {
	Chat struct {
		ID int64 `json:"id"`
	} `json:"chat"`
	Text string `json:"text"`
}

type callbackQuery struct {
	ID      string   `json:"id"`
	Data    string   `json:"data"`
	Message *message `json:"message"`
}

type apiResponse struct {
	OK          bool            `json:"ok"`
	Description string          `json:"description"`
	Result      json.RawMessage `json:"result"`
}

func New(token, category string, archiveChannel int64, st *store.Store) *Bot {
	if strings.TrimSpace(category) == "" {
		category = "电视剧"
	}
	return &Bot{token: strings.TrimSpace(token), category: category, archiveChannel: archiveChannel, store: st,
		client: &http.Client{Timeout: 40 * time.Second}}
}

func (b *Bot) Run(ctx context.Context) error {
	if b.token == "" {
		return errors.New("TV_BOT_TOKEN is empty")
	}
	log.Printf("[tvbot] started category=%s archive_channel=%d", b.category, b.archiveChannel)
	if b.archiveChannel != 0 {
		go b.archiveLoop(ctx)
	}
	for ctx.Err() == nil {
		if err := b.poll(ctx); err != nil && ctx.Err() == nil {
			log.Printf("[tvbot] poll: %v", err)
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(3 * time.Second):
			}
		}
	}
	return nil
}

func (b *Bot) poll(ctx context.Context) error {
	values := url.Values{"timeout": {"25"}, "offset": {strconv.Itoa(b.offset)}, "allowed_updates": {`["message","callback_query"]`}}
	var updates []update
	if err := b.call(ctx, "getUpdates", values, &updates); err != nil {
		return err
	}
	for _, u := range updates {
		b.offset = u.UpdateID + 1
		if u.Message != nil {
			b.handleMessage(ctx, u.Message)
		} else if u.CallbackQuery != nil {
			b.handleCallback(ctx, u.CallbackQuery)
		}
	}
	return nil
}

func (b *Bot) handleMessage(ctx context.Context, msg *message) {
	text := strings.TrimSpace(msg.Text)
	switch {
	case text == "/start" || text == "/help":
		b.send(ctx, msg.Chat.ID, "<b>📺 电视剧档案馆</b>\n\n/recent — 最近完结\n/search 剧名 — 搜索电视剧", nil)
	case text == "/recent":
		b.sendSeriesList(ctx, msg.Chat.ID, "最近完结", "")
	case strings.HasPrefix(text, "/search"):
		query := strings.TrimSpace(strings.TrimPrefix(text, "/search"))
		if query == "" {
			b.send(ctx, msg.Chat.ID, "请输入剧名，例如：<code>/search 权力的游戏</code>", nil)
			return
		}
		b.sendSeriesList(ctx, msg.Chat.ID, "搜索结果", query)
	default:
		if text != "" {
			b.sendSeriesList(ctx, msg.Chat.ID, "搜索结果", text)
		}
	}
}

func (b *Bot) sendSeriesList(ctx context.Context, chatID int64, heading, query string) {
	items, err := b.store.CompletedSeries(b.category, query, 10, 0)
	if err != nil {
		b.send(ctx, chatID, "读取电视剧档案失败，请稍后重试。", nil)
		return
	}
	if len(items) == 0 {
		b.send(ctx, chatID, "暂时没有匹配的完结电视剧。", nil)
		return
	}
	rows := make([][]map[string]string, 0, len(items))
	for _, item := range items {
		rows = append(rows, []map[string]string{{"text": item.Title, "callback_data": "tv:" + shortKey(item.SeriesKey)}})
	}
	b.send(ctx, chatID, "<b>"+html.EscapeString(heading)+"</b>\n请选择电视剧：", rows)
}

func (b *Bot) handleCallback(ctx context.Context, callback *callbackQuery) {
	_ = b.answerCallback(ctx, callback.ID)
	if callback.Message == nil {
		return
	}
	parts := strings.Split(callback.Data, ":")
	if len(parts) < 2 || (parts[0] != "tv" && parts[0] != "ep") {
		return
	}
	item, err := b.store.CompletedSeriesByPrefix(b.category, parts[1])
	if err != nil {
		b.send(ctx, callback.Message.Chat.ID, "该电视剧档案不存在或已更新。", nil)
		return
	}
	page := 0
	if len(parts) > 2 {
		page, _ = strconv.Atoi(parts[2])
	}
	text, keyboard := renderSeries(item, page, true)
	b.send(ctx, callback.Message.Chat.ID, text, keyboard)
}

func (b *Bot) archiveLoop(ctx context.Context) {
	b.syncArchive(ctx)
	ticker := time.NewTicker(2 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			b.syncArchive(ctx)
		}
	}
}

func (b *Bot) syncArchive(ctx context.Context) {
	items, err := b.store.CompletedSeries(b.category, "", 50, 0)
	if err != nil {
		log.Printf("[tvbot] archive query: %v", err)
		return
	}
	for _, item := range items {
		channelID, messageID, syncedAt, found, err := b.store.TVBotArchiveState(item.SeriesKey)
		if err != nil || (found && !item.UpdatedAt.After(syncedAt) && channelID == b.archiveChannel) {
			continue
		}
		text, _ := renderSeries(item, 0, false)
		if found && channelID == b.archiveChannel && messageID > 0 {
			if err := b.edit(ctx, channelID, messageID, text); err == nil {
				_ = b.store.SaveTVBotArchiveState(item.SeriesKey, channelID, messageID, item.UpdatedAt)
				continue
			}
		}
		newID, err := b.send(ctx, b.archiveChannel, text, nil)
		if err != nil {
			log.Printf("[tvbot] archive %s: %v", item.Title, err)
			continue
		}
		_ = b.store.SaveTVBotArchiveState(item.SeriesKey, b.archiveChannel, newID, item.UpdatedAt)
		log.Printf("[tvbot] archived %s message=%d", item.Title, newID)
	}
}

func renderSeries(item store.SeriesDirectoryEntry, page int, interactive bool) (string, [][]map[string]string) {
	const pageSize = 20
	if page < 0 {
		page = 0
	}
	start := page * pageSize
	if start >= len(item.Episodes) {
		start = 0
		page = 0
	}
	end := start + pageSize
	if end > len(item.Episodes) {
		end = len(item.Episodes)
	}
	var body strings.Builder
	fmt.Fprintf(&body, "<b>📺 %s</b>\n", html.EscapeString(item.Title))
	if item.Year != "" {
		fmt.Fprintf(&body, "年份：%s\n", html.EscapeString(item.Year))
	}
	fmt.Fprintf(&body, "状态：%s\n集数：%d\n\n", html.EscapeString(item.Remarks), len(item.Episodes))
	for _, episode := range item.Episodes {
		label := strings.TrimSpace(episode.Episode)
		if label == "" {
			label = fmt.Sprintf("第%02d集", episode.EpisodeIndex)
		}
		line := fmt.Sprintf("<a href=\"%s\">%s</a>  ", messageLink(item.ChannelID, episode.VideoMessageID), html.EscapeString(label))
		if body.Len()+len(line) > 3800 {
			break
		}
		body.WriteString(line)
	}
	if !interactive {
		return body.String(), nil
	}
	rows := make([][]map[string]string, 0, end-start+1)
	for _, episode := range item.Episodes[start:end] {
		label := strings.TrimSpace(episode.Episode)
		if label == "" {
			label = fmt.Sprintf("第%02d集", episode.EpisodeIndex)
		}
		rows = append(rows, []map[string]string{{"text": label, "url": messageLink(item.ChannelID, episode.VideoMessageID)}})
	}
	var nav []map[string]string
	if page > 0 {
		nav = append(nav, map[string]string{"text": "上一页", "callback_data": fmt.Sprintf("ep:%s:%d", shortKey(item.SeriesKey), page-1)})
	}
	if end < len(item.Episodes) {
		nav = append(nav, map[string]string{"text": "下一页", "callback_data": fmt.Sprintf("ep:%s:%d", shortKey(item.SeriesKey), page+1)})
	}
	if len(nav) > 0 {
		rows = append(rows, nav)
	}
	return body.String(), rows
}

func shortKey(key string) string {
	if len(key) > 24 {
		return key[:24]
	}
	return key
}

func messageLink(channelID int64, messageID int) string {
	id := strconv.FormatInt(channelID, 10)
	id = strings.TrimPrefix(id, "-100")
	return fmt.Sprintf("https://t.me/c/%s/%d", id, messageID)
}

func (b *Bot) send(ctx context.Context, chatID int64, text string, keyboard [][]map[string]string) (int, error) {
	values := url.Values{"chat_id": {strconv.FormatInt(chatID, 10)}, "text": {text}, "parse_mode": {"HTML"}, "disable_web_page_preview": {"true"}}
	if keyboard != nil {
		markup, _ := json.Marshal(map[string]interface{}{"inline_keyboard": keyboard})
		values.Set("reply_markup", string(markup))
	}
	var result struct {
		MessageID int `json:"message_id"`
	}
	err := b.call(ctx, "sendMessage", values, &result)
	return result.MessageID, err
}

func (b *Bot) edit(ctx context.Context, chatID int64, messageID int, text string) error {
	values := url.Values{"chat_id": {strconv.FormatInt(chatID, 10)}, "message_id": {strconv.Itoa(messageID)}, "text": {text}, "parse_mode": {"HTML"}, "disable_web_page_preview": {"true"}}
	return b.call(ctx, "editMessageText", values, nil)
}

func (b *Bot) answerCallback(ctx context.Context, id string) error {
	return b.call(ctx, "answerCallbackQuery", url.Values{"callback_query_id": {id}}, nil)
}

func (b *Bot) call(ctx context.Context, method string, values url.Values, target interface{}) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.telegram.org/bot"+b.token+"/"+method, strings.NewReader(values.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := b.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var envelope apiResponse
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return err
	}
	if !envelope.OK {
		return errors.New(envelope.Description)
	}
	if target != nil && len(envelope.Result) > 0 {
		return json.Unmarshal(envelope.Result, target)
	}
	return nil
}
