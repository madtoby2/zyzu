package poster

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/madtoby2/zyzu/internal/content"
	"github.com/madtoby2/zyzu/internal/store"
)

const tgAPI = "https://api.telegram.org/bot"

type Poster struct {
	token       string
	pickChannel func(cat string) int64
	client      *http.Client
}

func New(token string, pick func(string) int64) *Poster {
	return &Poster{
		token:       token,
		pickChannel: pick,
		client:      &http.Client{Timeout: 120 * time.Second},
	}
}

func (p *Poster) PostStation(st *store.Station, format string, action string) (int, error) {
	return p.sendMessage(p.formatStation(st, format, action), "Markdown", p.pickChannel("default"))
}

func (p *Poster) PostSimple(text string) (int, error) {
	return p.sendMessage(text, "", p.pickChannel("default"))
}

func (p *Poster) PostHTML(text string) (int, error) {
	return p.sendMessage(text, "HTML", p.pickChannel("default"))
}

func (p *Poster) PostVideo(filePath, caption, category string) (int, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return 0, fmt.Errorf("open video: %w", err)
	}
	defer file.Close()

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	w.WriteField("chat_id", fmt.Sprintf("%d", p.pickChannel(category)))
	w.WriteField("caption", caption)
	w.WriteField("parse_mode", "HTML")
	w.WriteField("supports_streaming", "true")
	part, _ := w.CreateFormFile("video", filepath.Base(filePath))
	io.Copy(part, file)
	w.Close()

	req, _ := http.NewRequest("POST", tgAPI+p.token+"/sendVideo", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())

	resp, err := p.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	respData, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	var result struct {
		OK     bool `json:"ok"`
		Result struct {
			MessageID int `json:"message_id"`
		} `json:"result"`
	}
	json.Unmarshal(respData, &result)
	if !result.OK {
		return 0, fmt.Errorf("sendVideo failed")
	}
	return result.Result.MessageID, nil
}

func (p *Poster) PostPhoto(photoURL, caption, category string) (int, error) {
	body := map[string]interface{}{
		"chat_id":    p.pickChannel(category),
		"photo":      photoURL,
		"caption":    caption,
		"parse_mode": "HTML",
	}
	data, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", tgAPI+p.token+"/sendPhoto", bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	respData, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	var result struct {
		OK     bool `json:"ok"`
		Result struct {
			MessageID int `json:"message_id"`
		} `json:"result"`
	}
	json.Unmarshal(respData, &result)
	return result.Result.MessageID, nil
}

func (p *Poster) PostContentDigest(items []content.ContentItem, title, category string) (int, error) {
	var sb strings.Builder
	sb.WriteString("<b>")
	sb.WriteString(title)
	sb.WriteString("</b>\n\n")
	for i, item := range items {
		if i >= 15 {
			break
		}
		sb.WriteString(fmt.Sprintf("%d. <b>%s</b>", i+1, escapeHTML(item.Title)))
		if item.TypeName != "" {
			sb.WriteString(fmt.Sprintf(" [%s]", item.TypeName))
		}
		sb.WriteString("\n")
		if len(item.Episodes) > 0 {
			parts := strings.SplitN(item.Episodes[0], "$", 2)
			if len(parts) == 2 {
				sb.WriteString(fmt.Sprintf("   🎬 <a href=\"%s\">%s</a>\n", parts[1], parts[0]))
			}
		}
		sb.WriteString("\n")
	}
	sb.WriteString(fmt.Sprintf("📊 共%d条 · ", len(items)))
	seen := map[string]bool{}
	sources := []string{}
	for _, item := range items {
		if !seen[item.Source] {
			sources = append(sources, item.Source)
			seen[item.Source] = true
		}
	}
	sb.WriteString(strings.Join(sources, " · "))
	return p.sendMessage(sb.String(), "HTML", p.pickChannel(category))
}

func (p *Poster) PostContentSplit(items []content.ContentItem) int {
	posted := 0
	for _, item := range items {
		cat := item.Category
		if cat == "" {
			cat = "default"
		}
		caption := fmt.Sprintf("<b>%s</b>", escapeHTML(item.Title))
		if item.TypeName != "" {
			caption += fmt.Sprintf(" | %s", item.TypeName)
		}
		caption += fmt.Sprintf("\n📡 %s", item.Source)
		for _, ep := range item.Episodes {
			parts := strings.SplitN(ep, "$", 2)
			if len(parts) == 2 {
				caption += fmt.Sprintf("\n🎬 <a href=\"%s\">%s</a>", parts[1], parts[0])
			}
		}
		if item.CoverURL != "" {
			_, err := p.PostPhoto(item.CoverURL, caption, cat)
			if err == nil {
				posted++
				time.Sleep(1500 * time.Millisecond)
				continue
			}
		}
		p.sendMessage(caption, "HTML", p.pickChannel(cat))
		posted++
		time.Sleep(time.Second)
	}
	return posted
}

func (p *Poster) PostVideoSplit(items []content.ContentItem) int {
	posted := 0
	for _, item := range items {
		if len(item.Episodes) == 0 {
			continue
		}
		parts := strings.SplitN(item.Episodes[0], "$", 2)
		if len(parts) != 2 {
			continue
		}
		cat := item.Category
		if cat == "" {
			cat = "default"
		}
		caption := fmt.Sprintf("<b>%s</b>", escapeHTML(item.Title))
		if item.TypeName != "" {
			caption += fmt.Sprintf(" | %s", item.TypeName)
		}
		caption += fmt.Sprintf("\n📡 %s", item.Source)
		_, err := p.PostVideo("", caption, cat) // placeholder — scheduler handles download
		_ = err
		posted++
		time.Sleep(2 * time.Second)
	}
	return posted
}

func (p *Poster) formatStation(st *store.Station, format string, action string) string {
	msg := format
	msg = strings.ReplaceAll(msg, "{name}", escapeMD(st.Name))
	msg = strings.ReplaceAll(msg, "{category}", st.Category)
	msg = strings.ReplaceAll(msg, "{api_url}", st.APIURL)
	msg = strings.ReplaceAll(msg, "{availability}", st.Availability)
	msg = strings.ReplaceAll(msg, "{resource_count}", st.ResourceCount)
	msg = strings.ReplaceAll(msg, "{response_time}", st.ResponseTime)
	msg = strings.ReplaceAll(msg, "{interface_type}", st.InterfaceType)
	msg = strings.ReplaceAll(msg, "{description}", st.Description)
	var tags []string
	json.Unmarshal([]byte(st.Tags), &tags)
	msg = strings.ReplaceAll(msg, "{tags}", strings.Join(tags, " · "))
	switch action {
	case "new":
		msg = "🆕 新站上线\n" + msg
	case "update":
		msg = "🔄 站点更新\n" + msg
	}
	return msg
}

func (p *Poster) sendMessage(text string, parseMode string, chatID int64) (int, error) {
	if chatID == 0 {
		return 0, nil
	}
	body := map[string]interface{}{
		"chat_id": chatID,
		"text":    text,
	}
	if parseMode != "" {
		body["parse_mode"] = parseMode
		body["disable_web_page_preview"] = true
	}
	data, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", tgAPI+p.token+"/sendMessage", bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	respData, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	var result struct {
		OK     bool `json:"ok"`
		Result struct {
			MessageID int `json:"message_id"`
		} `json:"result"`
	}
	json.Unmarshal(respData, &result)
	if !result.OK {
		return 0, fmt.Errorf("TG API error")
	}
	return result.Result.MessageID, nil
}

func escapeMD(s string) string {
	return strings.NewReplacer(
		"_", "\\_", "*", "\\*", "[", "\\[", "]", "\\]",
		"(", "\\(", ")", "\\)", "~", "\\~", "`", "\\`",
		">", "\\>", "#", "\\#", "+", "\\+", "-", "\\-",
		"=", "\\=", "|", "\\|", "{", "\\{", "}", "\\}",
		".", "\\.", "!", "\\!",
	).Replace(s)
}

func escapeHTML(s string) string {
	return strings.NewReplacer("<", "&lt;", ">", "&gt;", "&", "&amp;").Replace(s)
}
