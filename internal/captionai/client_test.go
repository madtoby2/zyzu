package captionai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/madtoby2/zyzu/internal/content"
)

func TestGenerateUsesUTF8AndReadsCaption(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if len(request.Messages) != 2 || !strings.Contains(request.Messages[1].Content, "测试影片") {
			t.Fatalf("UTF-8 prompt missing: %#v", request.Messages)
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"今夜的秘密，只留给愿意点开的人。"}}]}`))
	}))
	defer server.Close()

	client := New(server.URL, "secret", "test-model")
	got, err := client.Generate(context.Background(), content.ContentItem{Title: "测试影片"}, "成人")
	if err != nil {
		t.Fatal(err)
	}
	if got != "今夜的秘密，只留给愿意点开的人。" {
		t.Fatalf("Generate() = %q", got)
	}
}
