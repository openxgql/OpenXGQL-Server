package main

import (
	"regexp"
	"strings"
	"unicode/utf16"
)

var postURL = regexp.MustCompile(`https?://[^\s<>]+`)
var postTag = regexp.MustCompile(`#[\p{L}\p{N}_]+`)

func textLength(s string) int { return len(utf16.Encode([]rune(s))) }
func tweetTextEntities(text string) ([]map[string]any, []map[string]any) {
	urls, tags := []map[string]any{}, []map[string]any{}
	for _, span := range postURL.FindAllStringIndex(text, -1) {
		url := strings.TrimRight(text[span[0]:span[1]], ".,!?:;)]}")
		start := textLength(text[:span[0]])
		urls = append(urls, map[string]any{"url": url, "expanded_url": url, "display_url": strings.TrimPrefix(strings.TrimPrefix(url, "https://"), "http://"), "indices": []int{start, start + textLength(url)}})
	}
	for _, span := range postTag.FindAllStringIndex(text, -1) {
		tag := text[span[0]:span[1]]
		start := textLength(text[:span[0]])
		tags = append(tags, map[string]any{"text": strings.TrimPrefix(tag, "#"), "indices": []int{start, start + textLength(tag)}})
	}
	return urls, tags
}
