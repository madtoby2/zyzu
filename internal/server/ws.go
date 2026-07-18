package server

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

type WSHub struct {
	mu      sync.RWMutex
	clients map[*websocket.Conn]bool
}

func NewWSHub() *WSHub {
	return &WSHub{clients: make(map[*websocket.Conn]bool)}
}

func (h *WSHub) HandleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[ws] upgrade error: %v", err)
		return
	}

	h.mu.Lock()
	h.clients[conn] = true
	h.mu.Unlock()

	log.Printf("[ws] client connected (%d total)", len(h.clients))

	// Read loop (keep alive, handle close)
	go func() {
		defer func() {
			h.mu.Lock()
			delete(h.clients, conn)
			h.mu.Unlock()
			conn.Close()
			log.Printf("[ws] client disconnected (%d total)", len(h.clients))
		}()
		for {
			_, _, err := conn.ReadMessage()
			if err != nil {
				break
			}
		}
	}()
}

// Broadcast sends a JSON message to all connected WS clients.
func (h *WSHub) Broadcast(msgType string, data interface{}) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	msg := map[string]interface{}{
		"type": msgType,
		"data": data,
	}
	payload, _ := json.Marshal(msg)

	for conn := range h.clients {
		conn.WriteMessage(websocket.TextMessage, payload)
	}
}
