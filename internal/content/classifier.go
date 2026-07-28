package content

// Classify returns a category string for routing content to the right channel group.
// Categories: "adult", "anime", "movie", "variety", "default"
func Classify(typeName string, tags []string) string {
	t := typeName

	// Adult detection
	adultKeywords := []string{
		"情色", "成人", "AV", "伦理", "三级", "禁播", "无码", "有码", "番号",
		"中出", "内射", "国产自拍", "sex", "fuck", "pussy", "cock", "anal",
		"uncensored", "xxx", "blowjob", "cumshot", "masturbat", "dildo",
		"fingers her", "flicks her bean", "penetrat", "threesome", "horny",
		"banged", "handjob", "dick", "boobs", "tits",
	}
	for _, kw := range adultKeywords {
		if contains(t, kw) {
			return "adult"
		}
	}
	for _, tag := range tags {
		if tag == "泛娱乐" {
			return "adult"
		}
	}

	// Anime
	if contains(t, "动漫") || contains(t, "动画") {
		return "anime"
	}

	// Variety shows
	if contains(t, "综艺") {
		return "variety"
	}

	// TV series get their own channel group when configured.
	if contains(t, "电视剧") || contains(t, "连续剧") || contains(t, "网剧") ||
		contains(t, "剧") {
		return "tv"
	}

	// Movies
	if contains(t, "片") || contains(t, "电影") {
		return "movie"
	}

	return "default"
}

func contains(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		match := true
		for j := 0; j < len(sub); j++ {
			a := s[i+j]
			b := sub[j]
			if a >= 65 && a <= 90 {
				a += 32
			}
			if b >= 65 && b <= 90 {
				b += 32
			}
			if a != b {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
