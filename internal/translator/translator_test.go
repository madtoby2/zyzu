package translator

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

type memoryCache struct{ values map[string]string }

func (m *memoryCache) TranslationCacheGet(hash string) (string, bool, error) {
	v, ok := m.values[hash]
	return v, ok, nil
}
func (m *memoryCache) TranslationCachePut(hash, source, translated, provider string) error {
	m.values[hash] = translated
	return nil
}

func TestIntroUsesBingSessionAndPersistentCache(t *testing.T) {
	var translateCalls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/translator":
			w.Write([]byte(`<div data-iid="translator.5026"></div><script>var params_AbusePreventionHelper = [123456,"token-value"]; var x={IG:"ABCDEF123456"};</script>`))
		case "/ttranslatev3":
			atomic.AddInt32(&translateCalls, 1)
			if r.FormValue("token") != "token-value" || r.FormValue("key") != "123456" {
				t.Error("missing bing token")
			}
			w.Write([]byte(`[{"translations":[{"text":"一段中文简介"}]}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	cache := &memoryCache{values: map[string]string{}}
	tr := New(cache)
	tr.bingBase = server.URL
	for i := 0; i < 2; i++ {
		if got := tr.Intro("This is a foreign movie description."); got != "一段中文简介" {
			t.Fatalf("Intro=%q", got)
		}
	}
	if atomic.LoadInt32(&translateCalls) != 1 {
		t.Fatalf("translate calls=%d", translateCalls)
	}
}

func TestIntroKeepsChineseAndTooLongText(t *testing.T) {
	tr := New(nil)
	if got := tr.Intro("这是一段中文简介"); got != "这是一段中文简介" {
		t.Fatal(got)
	}
	long := strings.TrimSpace(strings.Repeat("foreign description ", 100))
	if got := tr.Intro(long); got != long {
		t.Fatal("overlong intro should remain unchanged")
	}
}
