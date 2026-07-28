package content

import "testing"

func TestClassifyAdultTitles(t *testing.T) {
	tests := []string{
		"Ann Hustle Fingers and Flicks Her Bean",
		"Hardcore Fucking With A Juicy Ass",
		"Uncensored-MXGS-1432",
		"无码中出作品",
	}
	for _, input := range tests {
		if got := Classify(input, nil); got != "adult" {
			t.Errorf("Classify(%q) = %q, want adult", input, got)
		}
	}
}

func TestClassifyGeneralVideo(t *testing.T) {
	tests := map[string]string{
		"动作电影":   "movie",
		"都市电视剧":  "tv",
		"日本动漫":   "anime",
		"真人综艺节目": "variety",
	}
	for input, want := range tests {
		if got := Classify(input, nil); got != want {
			t.Errorf("Classify(%q) = %q, want %q", input, got, want)
		}
	}
}
