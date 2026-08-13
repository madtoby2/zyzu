package translator

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
	"unicode"
)

// Translator provides best-effort, no-key intro translation. Translation is
// deliberately outside the critical path: failures return the original text.
type Translator struct {
	client   *http.Client
	endpoint string
	mu       sync.RWMutex
	cache    map[string]string
}

func New() *Translator {
	endpoint := strings.TrimSpace(os.Getenv("ZYZU_TRANSLATE_URL"))
	if endpoint == "" {
		endpoint = "https://api.mymemory.translated.net/get"
	}
	return &Translator{client: &http.Client{Timeout: 6 * time.Second}, endpoint: endpoint, cache: make(map[string]string)}
}

func (t *Translator) Intro(text string) string {
	text = strings.TrimSpace(text)
	source, ok := foreignLanguage(text)
	if !ok || len([]byte(text)) > 480 {
		return text
	}
	t.mu.RLock()
	translated, found := t.cache[text]
	t.mu.RUnlock()
	if found {
		return translated
	}
	u, err := url.Parse(t.endpoint)
	if err != nil {
		return text
	}
	q := u.Query()
	q.Set("q", text)
	q.Set("langpair", source+"|zh-CN")
	q.Set("mt", "1")
	u.RawQuery = q.Encode()
	resp, err := t.client.Get(u.String())
	if err != nil {
		return text
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return text
	}
	var result struct {
		ResponseData struct {
			TranslatedText string `json:"translatedText"`
		} `json:"responseData"`
		ResponseStatus json.RawMessage `json:"responseStatus"`
	}
	if json.NewDecoder(resp.Body).Decode(&result) != nil {
		return text
	}
	translated = strings.TrimSpace(result.ResponseData.TranslatedText)
	if translated == "" || strings.EqualFold(translated, text) {
		translated = text
	}
	t.mu.Lock()
	if len(t.cache) >= 1000 {
		t.cache = make(map[string]string)
	}
	t.cache[text] = translated
	t.mu.Unlock()
	return translated
}

func foreignLanguage(text string) (string, bool) {
	var han, latin, japanese, korean, cyrillic int
	for _, r := range text {
		switch {
		case unicode.In(r, unicode.Hiragana, unicode.Katakana):
			japanese++
		case unicode.In(r, unicode.Hangul):
			korean++
		case unicode.In(r, unicode.Cyrillic):
			cyrillic++
		case unicode.In(r, unicode.Han):
			han++
		case unicode.IsLetter(r) && r <= unicode.MaxLatin1:
			latin++
		}
	}
	if japanese > 1 {
		return "ja", true
	}
	if korean > 1 {
		return "ko", true
	}
	if cyrillic > 2 {
		return "ru", true
	}
	if latin >= 8 && latin > han*2 {
		return "en", true
	}
	return "", false
}
