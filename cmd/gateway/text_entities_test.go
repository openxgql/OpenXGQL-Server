package main

import "testing"

func TestTweetURLUsesUTF16Offsets(t *testing.T) {
	urls, tags := tweetTextEntities("⚡️ 😀 https://example.com/path. #тест")
	if len(urls) != 1 || urls[0]["url"] != "https://example.com/path" {
		t.Fatal(urls)
	}
	indices := urls[0]["indices"].([]int)
	if indices[0] != 6 || indices[1] != 30 {
		t.Fatal(indices)
	}
	if len(tags) != 1 || tags[0]["text"] != "тест" {
		t.Fatal(tags)
	}
}
