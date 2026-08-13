package translator

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"
)

type PersistentCache interface {
	TranslationCacheGet(hash string) (string, bool, error)
	TranslationCachePut(hash, source, translated, provider string) error
}

type bingSession struct {
	IG, IID, Key, Token string
	Expires             time.Time
}

type Translator struct {
	client       *http.Client
	cache        PersistentCache
	bingBase     string
	memoryURL    string
	mu           sync.Mutex
	bing         bingSession
	bingCooldown time.Time
}

var (
	keyTokenPattern = regexp.MustCompile(`params_AbusePreventionHelper\s*=\s*\[\s*([0-9]+)\s*,\s*"([^"]+)"`)
	igPattern       = regexp.MustCompile(`IG:\s*"([A-Fa-f0-9]+)"`)
	iidPattern      = regexp.MustCompile(`data-iid="([^"]+)"`)
)

func New(cache PersistentCache) *Translator {
	jar, _ := cookiejar.New(nil)
	return &Translator{client: &http.Client{Timeout: 8 * time.Second, Jar: jar}, cache: cache,
		bingBase: "https://www.bing.com", memoryURL: "https://api.mymemory.translated.net/get"}
}

func (t *Translator) Intro(text string) string {
	text = strings.TrimSpace(text)
	source, ok := foreignLanguage(text)
	if !ok {
		return text
	}
	hash := textHash(text)
	if t.cache != nil {
		if translated, found, err := t.cache.TranslationCacheGet(hash); err == nil && found {
			return translated
		}
	}
	translated, provider, err := t.translateBing(text, source)
	if err != nil && len([]byte(text)) <= 480 {
		translated, err = t.translateMyMemory(text, source)
		provider = "mymemory"
	}
	if err != nil || strings.TrimSpace(translated) == "" || strings.EqualFold(strings.TrimSpace(translated), text) {
		return text
	}
	translated = strings.TrimSpace(translated)
	if t.cache != nil {
		_ = t.cache.TranslationCachePut(hash, text, translated, provider)
	}
	return translated
}

func (t *Translator) translateBing(text, source string) (string, string, error) {
	if len([]rune(text)) > 1000 {
		return "", "", errors.New("bing text too long")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if time.Now().Before(t.bingCooldown) {
		return "", "", errors.New("bing cooling down")
	}
	if t.bing.Token == "" || time.Now().After(t.bing.Expires) {
		if err := t.refreshBing(); err != nil {
			return "", "", err
		}
	}
	translated, status, err := t.callBing(text, source)
	if status == http.StatusTooManyRequests {
		t.bingCooldown = time.Now().Add(30 * time.Minute)
		return "", "", errors.New("bing rate limited")
	}
	if err != nil && (status == http.StatusUnauthorized || status == http.StatusForbidden || status == http.StatusBadRequest) {
		t.bing = bingSession{}
		if refreshErr := t.refreshBing(); refreshErr == nil {
			translated, _, err = t.callBing(text, source)
		}
	}
	return translated, "bing", err
}

func (t *Translator) refreshBing() error {
	req, _ := http.NewRequest(http.MethodGet, t.bingBase+"/translator", nil)
	req.Header.Set("User-Agent", browserUA)
	resp, err := t.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	kt, ig, iid := keyTokenPattern.FindSubmatch(body), igPattern.FindSubmatch(body), iidPattern.FindSubmatch(body)
	if len(kt) < 3 || len(ig) < 2 {
		return errors.New("bing session parameters not found")
	}
	iidValue := "translator.5026"
	if len(iid) > 1 {
		iidValue = string(iid[1])
	}
	t.bing = bingSession{Key: string(kt[1]), Token: string(kt[2]), IG: string(ig[1]), IID: iidValue, Expires: time.Now().Add(20 * time.Minute)}
	return nil
}

func (t *Translator) callBing(text, source string) (string, int, error) {
	form := url.Values{"text": {text}, "fromLang": {source}, "to": {"zh-Hans"}, "token": {t.bing.Token}, "key": {t.bing.Key}, "tryFetchingGenderDebiasedTranslations": {"true"}}
	endpoint := fmt.Sprintf("%s/ttranslatev3?isVertical=1&IG=%s&IID=%s", t.bingBase, url.QueryEscape(t.bing.IG), url.QueryEscape(t.bing.IID))
	req, _ := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", browserUA)
	req.Header.Set("Referer", "https://www.bing.com/translator")
	resp, err := t.client.Do(req)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", resp.StatusCode, fmt.Errorf("bing HTTP %d", resp.StatusCode)
	}
	var result []struct {
		Translations []struct {
			Text string `json:"text"`
		} `json:"translations"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", resp.StatusCode, err
	}
	if len(result) == 0 || len(result[0].Translations) == 0 {
		return "", resp.StatusCode, errors.New("bing empty translation")
	}
	return result[0].Translations[0].Text, resp.StatusCode, nil
}

func (t *Translator) translateMyMemory(text, source string) (string, error) {
	u, _ := url.Parse(t.memoryURL)
	q := u.Query()
	q.Set("q", text)
	q.Set("langpair", source+"|zh-CN")
	q.Set("mt", "1")
	u.RawQuery = q.Encode()
	resp, err := t.client.Get(u.String())
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("mymemory HTTP %d", resp.StatusCode)
	}
	var result struct {
		ResponseData struct {
			TranslatedText string `json:"translatedText"`
		} `json:"responseData"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	return result.ResponseData.TranslatedText, nil
}

func textHash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

const browserUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/150 Safari/537.36"

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
