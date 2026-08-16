package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/madtoby2/zyzu/internal/config"
	"github.com/madtoby2/zyzu/internal/store"
	"github.com/madtoby2/zyzu/internal/tvbot"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	cfg, err := config.Load("config.json")
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	dbPath := envDefault("ZYZU_DB", "zyzu.db")
	st, err := store.New(dbPath)
	if err != nil {
		log.Fatalf("init store: %v", err)
	}
	defer st.Close()
	archiveChannel, _ := strconv.ParseInt(os.Getenv("TV_BOT_ARCHIVE_CHANNEL_ID"), 10, 64)
	token := os.Getenv("TV_BOT_TOKEN")
	if token == "" {
		token = cfg.BotToken
	}
	bot := tvbot.New(token, envDefault("TV_BOT_CATEGORY", "电视剧"), archiveChannel, st)
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	if err := bot.Run(ctx); err != nil {
		log.Fatal(err)
	}
}

func envDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
