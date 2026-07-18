package main

import (
	"embed"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/madtoby2/zyzu/internal/config"
	"github.com/madtoby2/zyzu/internal/handler"
	"github.com/madtoby2/zyzu/internal/poster"
	"github.com/madtoby2/zyzu/internal/scheduler"
	"github.com/madtoby2/zyzu/internal/scraper"
	"github.com/madtoby2/zyzu/internal/server"
	"github.com/madtoby2/zyzu/internal/store"
)

//go:embed web
var webFS embed.FS

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	log.Println("[zyzu] server v1.0")

	cfg, err := config.Load("config.json")
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	dbPath := "zyzu.db"
	if env := os.Getenv("ZYZU_DB"); env != "" {
		dbPath = env
	}
	st, err := store.New(dbPath)
	if err != nil {
		log.Fatalf("init store: %v", err)
	}
	defer st.Close()

	p := poster.New(cfg.BotToken, cfg.PickChannel)
	scr := scraper.New()
	sched := scheduler.New(st, scr, p, cfg)

	if cfg.BotToken != "" && cfg.HasAnyChannel() {
		if err := sched.Start(); err != nil {
			log.Printf("[zyzu] scheduler error: %v", err)
		}
	}

	hub := server.NewWSHub()

	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins: []string{"*"},
		AllowedMethods: []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders: []string{"*"},
	}))

	h := handler.New(st, sched, cfg, hub)
	h.Register(r)

	// Health check
	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})

	// Static web UI
	webSub, err := fs.Sub(webFS, "web")
	if err != nil {
		log.Fatalf("fs.Sub web: %v", err)
	}
	fileServer := http.FileServer(http.FS(webSub))
	r.Get("/", fileServer.ServeHTTP)
	r.NotFound(fileServer.ServeHTTP)

	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		<-sig
		log.Println("[zyzu] shutting down...")
		sched.Stop()
		st.Close()
		os.Exit(0)
	}()

	addr := cfg.ListenAddr
	if env := os.Getenv("ZYZU_ADDR"); env != "" {
		addr = env
	}
	log.Printf("[zyzu] listening on %s", addr)
	if err := http.ListenAndServe(addr, r); err != nil {
		log.Fatalf("server: %v", err)
	}
}
