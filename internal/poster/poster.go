package poster

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/madtoby2/zyzu/internal/store"
)

const tgAPI = "https://api.telegram.org/bot"

type Poster struct {
	token     string
	channelID int64
	client    *http.Client
}

func New(token string, channelID int64) *Poster {
	return &Poster{
		token:     token,
		channelID: channelID,
		client:    &http.Client{Timeout: 15 * time.Second},
	}
}

// PostStation formats and sends a station to the channel.
func (p *Poster) PostStation(st *store.Station, format string, action string) (int, error) {
	msg := p.formatMessage(st, format, action)
	return p.sendMessage(msg)
}

// PostSimple sends a plain text message.
func (p *Poster) PostSimple(text string) (int, error) {
	return p.sendMessage(text)
}

func (p *Poster) formatMessage(st *store.Station, format string, action string) string {
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

	if action == "new" {
		msg = "🆕 新站上线\n" + msg
	} else if action == "update" {
		msg = "🔄 站点更新\n" + msg
	}

	return msg
}

func (p *Poster) sendMessage(text string) (int, error) {
	body := map[string]interface{}{
		"chat_id":    p.channelID,
		"text":       text,
		"parse_mode": "Markdown",
	}
	data, _ := json.Marshal(body)

	url := tgAPI + p.token + "/sendMessage"
	req, _ := http.NewRequest("POST", url, bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	respData, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode != 200 {
		return 0, fmt.Errorf("TG API %d: %s", resp.StatusCode, string(respData))
	}

	var result struct {
		OK     bool `json:"ok"`
		Result struct {
			MessageID int `json:"message_id"`
		} `json:"result"`
	}
	if err := json.Unmarshal(respData, &result); err != nil {
		return 0, err
	}
	if !result.OK {
		return 0, fmt.Errorf("TG API error: %s", string(respData))
	}
	return result.Result.MessageID, nil
}

func escapeMD(s string) string {
	replacer := strings.NewReplacer(
		"_", "\\_", "*", "\\*", "[", "\\[", "]", "\\]",
		"(", "\\(", ")", "\\)", "~", "\\~", "`", "\\`",
		">", "\\>", "#", "\\#", "+", "\\+", "-", "\\-",
		"=", "\\=", "|", "\\|", "{", "\\{", "}", "\\}",
		".", "\\.", "!", "\\!",
	)
	return replacer.Replace(s)
}
