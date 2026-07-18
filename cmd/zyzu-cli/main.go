package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

type Profile struct {
	Name   string `json:"name"`
	Server string `json:"server"`
	Key    string `json:"key"`
}

type ConfigFile struct {
	Active  string    `json:"active"`
	Profiles []Profile `json:"profiles"`
}

var cfg ConfigFile

func configPath() string {
	dir, _ := os.UserConfigDir()
	return filepath.Join(dir, "zyzu.json")
}

func loadConfig() {
	data, err := os.ReadFile(configPath())
	if err == nil {
		json.Unmarshal(data, &cfg)
	}
}

func saveConfig() {
	os.MkdirAll(filepath.Dir(configPath()), 0755)
	data, _ := json.MarshalIndent(cfg, "", "  ")
	os.WriteFile(configPath(), data, 0644)
}

func activeProfile() *Profile {
	for i := range cfg.Profiles {
		if cfg.Profiles[i].Name == cfg.Active {
			return &cfg.Profiles[i]
		}
	}
	return nil
}

func main() {
	loadConfig()

	if len(os.Args) < 2 {
		usage()
		return
	}

	// Override from env if no profiles exist
	p := activeProfile()
	serverURL := os.Getenv("ZYZU_SERVER")
	apiKey := os.Getenv("ZYZU_KEY")
	if p != nil {
		if serverURL == "" {
			serverURL = p.Server
		}
		if apiKey == "" {
			apiKey = p.Key
		}
	}
	if serverURL == "" {
		serverURL = "http://localhost:8080"
	}

	cmd := os.Args[1]
	switch cmd {
	case "login":
		cmdLogin()
		return
	case "use":
		if len(os.Args) < 3 {
			fmt.Println("usage: zyzu-cli use <profile-name>")
			listProfiles()
			return
		}
		cmdUse(os.Args[2])
		return
	case "profiles":
		listProfiles()
		return
	}

	// All other commands need a server connection
	if p == nil && serverURL == "http://localhost:8080" && apiKey == "" {
		fmt.Println("未配置服务器。请先运行: zyzu-cli login")
		return
	}

	switch cmd {
	case "list":
		cmdList(serverURL, apiKey)
	case "stats":
		cmdStats(serverURL, apiKey)
	case "block":
		if len(os.Args) < 3 {
			fmt.Println("usage: zyzu-cli block <slug>")
			return
		}
		cmdBlock(serverURL, apiKey, os.Args[2], true)
	case "unblock":
		if len(os.Args) < 3 {
			fmt.Println("usage: zyzu-cli unblock <slug>")
			return
		}
		cmdBlock(serverURL, apiKey, os.Args[2], false)
	case "post":
		if len(os.Args) < 3 {
			fmt.Println("usage: zyzu-cli post <slug>")
			return
		}
		cmdPost(serverURL, apiKey, os.Args[2])
	case "trigger":
		cmdTrigger(serverURL, apiKey)
	case "content":
		cmdContent(serverURL, apiKey)
	case "status":
		cmdStatus(serverURL, apiKey)
	case "history":
		cmdHistory(serverURL, apiKey)
	default:
		usage()
	}
}

func usage() {
	fmt.Println(`zyzu-cli — 资源组 TG频道管理客户端

用法:
  zyzu-cli login              添加/修改服务器配置
  zyzu-cli use <name>          切换当前服务器
  zyzu-cli profiles            列出所有配置
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
  ZYZU_SERVER  服务器地址 (覆盖配置文件)
  ZYZU_KEY     API Key (覆盖配置文件)`)
}

func cmdLogin() {
	reader := bufio.NewReader(os.Stdin)
	fmt.Print("配置名称 [default]: ")
	name, _ := reader.ReadString('\n')
	name = strings.TrimSpace(name)
	if name == "" {
		name = "default"
	}

	fmt.Print("Server URL: ")
	url, _ := reader.ReadString('\n')
	url = strings.TrimSpace(url)

	fmt.Print("API Key: ")
	key, _ := reader.ReadString('\n')
	key = strings.TrimSpace(key)

	// Test connection
	resp, err := http.Get(url + "/health")
	if err != nil {
		fmt.Printf("连接失败: %v\n", err)
		return
	}
	resp.Body.Close()

	// Save profile
	found := false
	for i := range cfg.Profiles {
		if cfg.Profiles[i].Name == name {
			cfg.Profiles[i].Server = url
			cfg.Profiles[i].Key = key
			found = true
			break
		}
	}
	if !found {
		cfg.Profiles = append(cfg.Profiles, Profile{Name: name, Server: url, Key: key})
	}
	cfg.Active = name
	saveConfig()

	fmt.Printf("✓ 已保存 [%s] → %s\n", name, url)
}

func cmdUse(name string) {
	for _, p := range cfg.Profiles {
		if p.Name == name {
			cfg.Active = name
			saveConfig()
			fmt.Printf("✓ 当前服务器: [%s] %s\n", name, p.Server)
			return
		}
	}
	fmt.Printf("未找到配置: %s\n", name)
	listProfiles()
}

func listProfiles() {
	if len(cfg.Profiles) == 0 {
		fmt.Println("暂无配置，请运行: zyzu-cli login")
		return
	}
	for _, p := range cfg.Profiles {
		mark := " "
		if p.Name == cfg.Active {
			mark = "*"
		}
		fmt.Printf(" %s %-12s %s\n", mark, p.Name, p.Server)
	}
}

func cmdList(server, key string) {
	var resp struct {
		OK   bool           `json:"ok"`
		Data []stationBrief `json:"data"`
	}
	if err := apiGetJSON(server, key, "/api/stations?all=1", &resp); err != nil {
		fmt.Println("错误:", err)
		return
	}
	fmt.Printf("%-4s %-18s %-8s %-6s %-6s %s\n", "ID", "名称", "资源量", "可用率", "响应", "Slug")
	for _, s := range resp.Data {
		mark := " "
		if s.Blacklisted {
			mark = "🚫"
		}
		fmt.Printf("%-4d %s%-16s %-8s %-6s %-6s %s\n", s.ID, mark, s.Name, s.ResourceCount, s.Availability, s.ResponseTime, s.Slug)
	}
}

func cmdStats(server, key string) {
	var resp struct {
		OK   bool                   `json:"ok"`
		Data map[string]interface{} `json:"data"`
	}
	apiGetJSON(server, key, "/api/stations/stats", &resp)
	fmt.Printf("总站点: %.0f  已屏蔽: %.0f  已推送: %.0f\n",
		resp.Data["total"], resp.Data["blacklisted"], resp.Data["posted"])
}

func cmdBlock(server, key, slug string, blocked bool) {
	action := "屏蔽"
	if !blocked {
		action = "解除"
	}
	var resp map[string]interface{}
	body := map[string]bool{"blacklisted": blocked}
	if err := apiPostJSON(server, key, "/api/stations/"+slug+"/blacklist", body, &resp); err != nil {
		fmt.Printf("%s失败: %v\n", action, err)
		return
	}
	fmt.Printf("✓ 已%s: %s\n", action, slug)
}

func cmdPost(server, key, slug string) {
	var resp struct {
		OK    bool   `json:"ok"`
		Data  struct {
			MessageID int `json:"message_id"`
		} `json:"data"`
		Error string `json:"error"`
	}
	apiPostJSON(server, key, "/api/stations/"+slug+"/post", nil, &resp)
	if resp.OK {
		fmt.Printf("✓ 已推送到TG, MSG #%d\n", resp.Data.MessageID)
	} else {
		fmt.Printf("推送失败: %s\n", resp.Error)
	}
}

func cmdTrigger(server, key string) {
	if err := apiPost(server, key, "/api/trigger"); err != nil {
		fmt.Println("失败:", err)
		return
	}
	fmt.Println("✓ 采集已触发")
}

func cmdContent(server, key string) {
	if err := apiPost(server, key, "/api/content/trigger"); err != nil {
		fmt.Println("失败:", err)
		return
	}
	fmt.Println("✓ 内容抓取已触发")
}

func cmdStatus(server, key string) {
	var resp struct {
		OK   bool                   `json:"ok"`
		Data map[string]interface{} `json:"data"`
	}
	apiGetJSON(server, key, "/api/status", &resp)
	d := resp.Data
	fmt.Printf("调度: running=%v  last_run=%v\n", d["running"], d["last_run"])
	fmt.Printf("站: new=%v updated=%v | 内容: %v\n", d["new_count"], d["upd_count"], d["content_count"])
	fmt.Printf("cron: scrape=%v content=%v mode=%v\n", d["cron_scrape"], d["cron_content"], d["content_mode"])
	if d["last_error"] != "" {
		fmt.Printf("错误: %v\n", d["last_error"])
	}
}

func cmdHistory(server, key string) {
	var resp struct {
		OK   bool      `json:"ok"`
		Data []postLog `json:"data"`
	}
	apiGetJSON(server, key, "/api/history", &resp)
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

func apiGet(server, key, path string) (*http.Response, error) {
	req, _ := http.NewRequest("GET", server+path, nil)
	if key != "" {
		req.Header.Set("X-API-Key", key)
	}
	return http.DefaultClient.Do(req)
}

func apiGetJSON(server, key, path string, v interface{}) error {
	resp, err := apiGet(server, key, path)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return json.NewDecoder(resp.Body).Decode(v)
}

func apiPost(server, key, path string) error {
	req, _ := http.NewRequest("POST", server+path, nil)
	if key != "" {
		req.Header.Set("X-API-Key", key)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
}

func apiPostJSON(server, key, path string, body interface{}, v interface{}) error {
	var r io.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		r = bytes.NewReader(data)
	}
	req, _ := http.NewRequest("POST", server+path, r)
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("X-API-Key", key)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return json.NewDecoder(resp.Body).Decode(v)
}
