package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

var (
	serverURL string
	apiKey    string
)

func main() {
	if len(os.Args) < 2 {
		usage()
		return
	}

	serverURL = os.Getenv("ZYZU_SERVER")
	if serverURL == "" {
		serverURL = "http://localhost:8080"
	}
	apiKey = os.Getenv("ZYZU_KEY")

	cmd := os.Args[1]
	switch cmd {
	case "login":
		cmdLogin()
	case "list":
		cmdList()
	case "stats":
		cmdStats()
	case "block":
		if len(os.Args) < 3 {
			fmt.Println("usage: zyzu-cli block <slug>")
			return
		}
		cmdBlock(os.Args[2], true)
	case "unblock":
		if len(os.Args) < 3 {
			fmt.Println("usage: zyzu-cli unblock <slug>")
			return
		}
		cmdBlock(os.Args[2], false)
	case "post":
		if len(os.Args) < 3 {
			fmt.Println("usage: zyzu-cli post <slug>")
			return
		}
		cmdPost(os.Args[2])
	case "trigger":
		cmdTrigger()
	case "content":
		cmdContent()
	case "status":
		cmdStatus()
	case "history":
		cmdHistory()
	default:
		usage()
	}
}

func usage() {
	fmt.Println(`zyzu-cli — 资源组 TG频道管理客户端

用法:
  zyzu-cli login              设置服务器地址和API Key
  zyzu-cli list                列出所有资源站
  zyzu-cli stats               统计信息
  zyzu-cli block <slug>        屏蔽某站
  zyzu-cli unblock <slug>      解除屏蔽
  zyzu-cli post <slug>         手动推送到频道
  zyzu-cli trigger             立即触发站采集
  zyzu-cli content             立即触发内容抓取
  zyzu-cli status              调度状态
  zyzu-cli history             推送历史

环境变量:
  ZYZU_SERVER  服务器地址 (默认 http://localhost:8080)
  ZYZU_KEY     API Key`)
}

func cmdLogin() {
	reader := bufio.NewReader(os.Stdin)
	fmt.Print("Server URL [http://localhost:8080]: ")
	url, _ := reader.ReadString('\n')
	url = strings.TrimSpace(url)
	if url != "" {
		serverURL = url
	}
	fmt.Print("API Key: ")
	key, _ := reader.ReadString('\n')
	apiKey = strings.TrimSpace(key)

	// Test connection
	resp, err := apiGet("/health")
	if err != nil {
		fmt.Printf("连接失败: %v\n", err)
		return
	}
	fmt.Printf("✓ 已连接 %s (HTTP %d)\n", serverURL, resp.StatusCode)
	fmt.Printf("export ZYZU_SERVER=%s\n", serverURL)
	fmt.Printf("export ZYZU_KEY=%s\n", apiKey)
}

func cmdList() {
	var resp struct {
		OK   bool            `json:"ok"`
		Data []stationBrief   `json:"data"`
	}
	if err := apiGetJSON("/api/stations?all=1", &resp); err != nil {
		fmt.Println("错误:", err)
		return
	}
	fmt.Printf("%-4s %-18s %-8s %-6s %-6s %s\n", "ID", "名称", "资源量", "可用率", "响应", "标签")
	for _, s := range resp.Data {
		mark := " "
		if s.Blacklisted {
			mark = "🚫"
		}
		fmt.Printf("%-4d %s%-16s %-8s %-6s %-6s %s\n", s.ID, mark, s.Name, s.ResourceCount, s.Availability, s.ResponseTime, s.Slug)
	}
}

func cmdStats() {
	var resp struct {
		OK   bool              `json:"ok"`
		Data map[string]interface{} `json:"data"`
	}
	apiGetJSON("/api/stations/stats", &resp)
	fmt.Printf("总站点: %.0f  已屏蔽: %.0f  已推送: %.0f\n",
		resp.Data["total"], resp.Data["blacklisted"], resp.Data["posted"])
}

func cmdBlock(slug string, blocked bool) {
	action := "屏蔽"
	if !blocked {
		action = "解除"
	}
	var resp map[string]interface{}
	body := map[string]bool{"blacklisted": blocked}
	if err := apiPostJSON("/api/stations/"+slug+"/blacklist", body, &resp); err != nil {
		fmt.Printf("%s失败: %v\n", action, err)
		return
	}
	fmt.Printf("✓ 已%s: %s\n", action, slug)
}

func cmdPost(slug string) {
	var resp struct {
		OK   bool `json:"ok"`
		Data struct {
			MessageID int `json:"message_id"`
		} `json:"data"`
		Error string `json:"error"`
	}
	apiPostJSON("/api/stations/"+slug+"/post", nil, &resp)
	if resp.OK {
		fmt.Printf("✓ 已推送到TG, MSG #%d\n", resp.Data.MessageID)
	} else {
		fmt.Printf("推送失败: %s\n", resp.Error)
	}
}

func cmdTrigger() {
	apiPost("/api/trigger")
	fmt.Println("✓ 采集已触发")
}

func cmdContent() {
	apiPost("/api/content/trigger")
	fmt.Println("✓ 内容抓取已触发")
}

func cmdStatus() {
	var resp struct {
		OK   bool              `json:"ok"`
		Data map[string]interface{} `json:"data"`
	}
	apiGetJSON("/api/status", &resp)
	d := resp.Data
	fmt.Printf("调度: running=%v  last_run=%v\n", d["running"], d["last_run"])
	fmt.Printf("站: new=%v updated=%v | 内容: %v\n", d["new_count"], d["upd_count"], d["content_count"])
	fmt.Printf("cron: scrape=%v content=%v mode=%v\n", d["cron_scrape"], d["cron_content"], d["content_mode"])
	if d["last_error"] != "" {
		fmt.Printf("错误: %v\n", d["last_error"])
	}
}

func cmdHistory() {
	var resp struct {
		OK   bool      `json:"ok"`
		Data []postLog  `json:"data"`
	}
	apiGetJSON("/api/history", &resp)
	fmt.Printf("%-20s %-20s %-8s %s\n", "时间", "站点", "动作", "MSG ID")
	for _, h := range resp.Data {
		fmt.Printf("%-20s %-20s %-8s %d\n", h.PostedAt[:19], h.Content, h.Action, h.MessageID)
	}
}

// --- helpers ---

type stationBrief struct {
	ID            int    `json:"id"`
	Slug          string `json:"slug"`
	Name          string `json:"name"`
	ResourceCount string `json:"resource_count"`
	Availability  string `json:"availability"`
	ResponseTime  string `json:"response_time"`
	Blacklisted   bool   `json:"blacklisted"`
}

type postLog struct {
	PostedAt  string `json:"posted_at"`
	Content   string `json:"content"`
	Action    string `json:"action"`
	MessageID int    `json:"message_id"`
}

func apiGet(path string) (*http.Response, error) {
	req, _ := http.NewRequest("GET", serverURL+path, nil)
	if apiKey != "" {
		req.Header.Set("X-API-Key", apiKey)
	}
	return http.DefaultClient.Do(req)
}

func apiGetJSON(path string, v interface{}) error {
	resp, err := apiGet(path)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return json.NewDecoder(resp.Body).Decode(v)
}

func apiPost(path string) error {
	req, _ := http.NewRequest("POST", serverURL+path, nil)
	if apiKey != "" {
		req.Header.Set("X-API-Key", apiKey)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
}

func apiPostJSON(path string, body interface{}, v interface{}) error {
	var r io.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		r = bytes.NewReader(data)
	}
	req, _ := http.NewRequest("POST", serverURL+path, r)
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("X-API-Key", apiKey)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return json.NewDecoder(resp.Body).Decode(v)
}
