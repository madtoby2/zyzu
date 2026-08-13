package translator

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestIntroTranslatesForeignTextAndCaches(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		if got := r.URL.Query().Get("langpair"); got != "en|zh-CN" {
			t.Errorf("langpair=%q", got)
		}
		w.Write([]byte(`{"responseData":{"translatedText":"一段中文简介"},"responseStatus":200}`))
	}))
	defer server.Close()
	tr := New()
	tr.endpoint = server.URL
	for i := 0; i < 2; i++ {
		if got := tr.Intro("This is a foreign movie description."); got != "一段中文简介" {
			t.Fatalf("Intro=%q", got)
		}
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("calls=%d", calls)
	}
}

func TestIntroKeepsChineseAndLongText(t *testing.T) {
	tr := New()
	if got := tr.Intro("这是一段中文简介"); got != "这是一段中文简介" {
		t.Fatal(got)
	}
	long := strings.TrimSpace(strings.Repeat("foreign description ", 40))
	if got := tr.Intro(long); got != long {
		t.Fatal("long intro should remain unchanged")
	}
}
