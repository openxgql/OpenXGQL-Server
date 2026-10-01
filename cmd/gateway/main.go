package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"flashlarry-api/internal/common"
)

type User struct {
	ID              string    `json:"id"`
	ScreenName      string    `json:"screen_name"`
	Name            string    `json:"name"`
	Bio             string    `json:"bio"`
	Location        string    `json:"location"`
	Website         string    `json:"website"`
	AvatarURL       string    `json:"avatar_url"`
	Verified        bool      `json:"verified"`
	CreatedAt       time.Time `json:"created_at"`
	Followers       int64     `json:"followers,omitempty"`
	Following       int64     `json:"following,omitempty"`
	IsFollowing     bool      `json:"is_following,omitempty"`
	IsFollowedBy    bool      `json:"is_followed_by,omitempty"`
	Protected       bool      `json:"protected"`
	Blocking        bool      `json:"blocking"`
	BlockedBy       bool      `json:"blocked_by"`
	Muting          bool      `json:"muting"`
	FollowRequested bool      `json:"follow_request_sent"`
	Pinned          []string  `json:"pinned"`
}

type Tweet struct {
	ID        string    `json:"id"`
	AuthorID  string    `json:"author_id"`
	Text      string    `json:"text"`
	ReplyToID *string   `json:"reply_to_id"`
	CreatedAt time.Time `json:"created_at"`
	Media     []Media   `json:"media,omitempty"`
}

type Media struct {
	ID   string `json:"id"`
	MIME string `json:"mime"`
	URL  string `json:"url"`
	Alt  string `json:"alt"`
}

type Gateway struct {
	client        *http.Client
	usersURL      string
	tweetsURL     string
	socialURL     string
	internalToken string
	flowMu        sync.Mutex
	flows         map[string]mobileFlow
}

type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string { return e.Message }

func main() {
	g := &Gateway{
		client:        &http.Client{Timeout: 5 * time.Second},
		usersURL:      common.Env("USERS_URL", "http://127.0.0.1:8081"),
		tweetsURL:     common.Env("TWEETS_URL", "http://127.0.0.1:8082"),
		socialURL:     common.Env("SOCIAL_URL", "http://127.0.0.1:8083"),
		internalToken: common.Env("SOCIAL_INTERNAL_TOKEN", ""),
		flows:         make(map[string]mobileFlow),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", g.health)
	mux.HandleFunc("GET /1.1/help/settings.json", g.webFeatureSettings)
	mux.Handle("POST /1.1/users/report_spam.json", g.requiredAuth(http.HandlerFunc(g.reportSpam)))
	mux.Handle("POST /web/reports", g.requiredAuth(http.HandlerFunc(g.reportPost)))
	mux.HandleFunc("GET /ads/click/{delivery}", g.adClick)
	mux.Handle("GET /i/api/graphql/{queryID}/{operation}", g.requiredAuth(http.HandlerFunc(g.internalGraphQL)))
	mux.Handle("POST /i/api/graphql/{queryID}/{operation}", g.requiredAuth(http.HandlerFunc(g.internalGraphQL)))
	mux.Handle("GET /graphql/{queryID}/{operation}", g.requiredMobileAuth(http.HandlerFunc(g.mobileGraphQL)))
	mux.Handle("POST /graphql/{queryID}/{operation}", g.requiredMobileAuth(http.HandlerFunc(g.mobileGraphQL)))
	mux.HandleFunc("POST /oauth2/token", g.mobileOAuthToken)
	mux.HandleFunc("POST /1.1/guest/activate.json", g.mobileGuest)
	mux.HandleFunc("POST /1.1/onboarding/task.json", g.mobileOnboarding)
	mux.Handle("POST /1.1/account/update_profile.json", g.requiredMobileAuth(http.HandlerFunc(g.mobileUpdateProfile)))
	mux.Handle("POST /1.1/account/update_profile_image.json", g.requiredMobileAuth(http.HandlerFunc(g.updateProfileImage)))
	mux.HandleFunc("POST /web/auth/email/start", g.webEmailStart)
	mux.HandleFunc("POST /web/auth/email/verify", g.webEmailVerify)
	mux.HandleFunc("GET /web/auth/config", g.webAuthConfig)
	mux.HandleFunc("GET /web/auth/session", g.webSession)
	mux.HandleFunc("POST /web/auth/logout", g.webLogout)
	mux.HandleFunc("POST /1.1/account/logout.json", g.webLogout)
	mux.HandleFunc("POST /1.1/account/multi/logout_all.json", g.webLogout)
	mux.Handle("GET /1.1/account/settings.json", g.requiredAuth(http.HandlerFunc(g.webAccountSettings)))
	mux.Handle("POST /i/media/upload.json", g.requiredAuth(http.HandlerFunc(g.mediaUpload)))
	mux.Handle("GET /i/media/upload.json", g.requiredAuth(http.HandlerFunc(g.mediaUpload)))
	mux.Handle("POST /i/media/upload2.json", g.requiredAuth(http.HandlerFunc(g.mediaUpload)))
	mux.Handle("GET /i/media/upload2.json", g.requiredAuth(http.HandlerFunc(g.mediaUpload)))
	mux.Handle("POST /1.1/media/upload.json", g.requiredMobileAuth(http.HandlerFunc(g.mediaUpload)))
	mux.Handle("GET /1.1/media/upload.json", g.requiredMobileAuth(http.HandlerFunc(g.mediaUpload)))
	mux.Handle("POST /1.1/media/metadata/create.json", g.requiredMobileAuth(http.HandlerFunc(g.mediaMetadata)))
	mux.HandleFunc("GET /media/{id}", g.serveMedia)
	mux.HandleFunc("GET /2/guide.json", g.webGuide)
	mux.HandleFunc("GET /fleets/v1/fleetline", g.webFleetline)
	mux.HandleFunc("POST /1.1/flow/viewer.json", g.webTelemetry)
	mux.HandleFunc("POST /1.1/graphql/viewer_context.json", g.webTelemetry)
	common.Serve(common.Env("LISTEN_ADDR", "127.0.0.1:8080"), mux)
}

func (g *Gateway) health(w http.ResponseWriter, r *http.Request) {
	for _, base := range []string{g.usersURL, g.tweetsURL} {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/healthz", nil)
		response, err := g.client.Do(req)
		cancel()
		if err != nil {
			common.Error(w, 503, "dependency unavailable")
			return
		}
		_ = response.Body.Close()
		if response.StatusCode != 200 {
			common.Error(w, 503, "dependency unavailable")
			return
		}
	}
	common.WriteJSON(w, 200, map[string]string{"status": "ok"})
}

func (g *Gateway) call(ctx context.Context, method, endpoint string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if g.socialURL != "" && strings.HasPrefix(endpoint, g.socialURL+"/") {
		req.Header.Set("X-Internal-Token", g.internalToken)
	}
	response, err := g.client.Do(req)
	if err != nil {
		return &APIError{Status: 502, Message: "backend unavailable"}
	}
	defer response.Body.Close()
	if response.StatusCode >= 400 {
		var result struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&result)
		if result.Error == "" {
			result.Error = "backend request failed"
		}
		return &APIError{Status: response.StatusCode, Message: result.Error}
	}
	if out != nil && response.StatusCode != 204 {
		if err := json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(out); err != nil {
			return &APIError{Status: 502, Message: "invalid backend response"}
		}
	}
	return nil
}

func (g *Gateway) userByName(ctx context.Context, name string) (User, error) {
	var user User
	err := g.call(ctx, "GET", g.usersURL+"/users/by-name/"+url.PathEscape(name), nil, &user)
	if err == nil {
		err = g.enrichUser(ctx, &user)
	}
	return user, err
}

func (g *Gateway) userByID(ctx context.Context, id string) (User, error) {
	var user User
	err := g.call(ctx, "GET", g.usersURL+"/users/"+url.PathEscape(id), nil, &user)
	if err == nil {
		err = g.enrichUser(ctx, &user)
	}
	return user, err
}

func (g *Gateway) enrichUser(ctx context.Context, user *User) error {
	viewerID := ""
	if actor := actorFrom(ctx); actor != nil {
		viewerID = actor.UserID
	}
	var relation struct {
		Followers    int64 `json:"followers"`
		Following    int64 `json:"following"`
		IsFollowing  bool  `json:"is_following"`
		IsFollowedBy bool  `json:"is_followed_by"`
	}
	endpoint := g.usersURL + "/follows/" + url.PathEscape(user.ID) + "/relationship?viewer_id=" + url.QueryEscape(viewerID)
	if err := g.call(ctx, http.MethodGet, endpoint, nil, &relation); err != nil {
		return err
	}
	user.Followers, user.Following = relation.Followers, relation.Following
	user.IsFollowing, user.IsFollowedBy = relation.IsFollowing, relation.IsFollowedBy
	if g.socialURL != "" {
		var state User
		if err := g.call(ctx, "GET", g.socialURL+"/users/"+url.PathEscape(user.ID)+"/state?viewer_id="+url.QueryEscape(viewerID), nil, &state); err != nil {
			return err
		}
		user.Protected, user.Blocking, user.BlockedBy, user.Muting, user.FollowRequested, user.Pinned = state.Protected, state.Blocking, state.BlockedBy, state.Muting, state.FollowRequested, state.Pinned
	}
	return nil
}

func (g *Gateway) tweetByID(ctx context.Context, id string) (Tweet, error) {
	if err := g.checkPostAccess(ctx, id); err != nil {
		return Tweet{}, err
	}
	var tweet Tweet
	err := g.call(ctx, "GET", g.tweetsURL+"/tweets/"+url.PathEscape(id), nil, &tweet)
	return tweet, err
}

func (g *Gateway) createTweet(ctx context.Context, authorID, text string, replyToID *string, mediaIDs ...string) (Tweet, error) {
	if _, err := g.userByID(ctx, authorID); err != nil {
		return Tweet{}, err
	}
	var tweet Tweet
	err := g.call(ctx, "POST", g.tweetsURL+"/tweets", map[string]any{"author_id": authorID, "text": text, "reply_to_id": replyToID, "media_ids": mediaIDs}, &tweet)
	return tweet, err
}

func (g *Gateway) deleteTweet(ctx context.Context, authorID, tweetID string) (bool, error) {
	endpoint := g.tweetsURL + "/tweets/" + url.PathEscape(tweetID) + "?author_id=" + url.QueryEscape(authorID)
	var result struct {
		Deleted bool `json:"deleted"`
	}
	err := g.call(ctx, "DELETE", endpoint, nil, &result)
	return result.Deleted, err
}

func (g *Gateway) tweetsByUser(ctx context.Context, id string, count int, mode string) ([]Tweet, error) {
	endpoint := g.tweetsURL + "/tweets?author_id=" + url.QueryEscape(id) + "&limit=" + strconv.Itoa(count) + "&mode=" + url.QueryEscape(mode)
	var tweets []Tweet
	err := g.call(ctx, "GET", endpoint, nil, &tweets)
	return tweets, err
}

func (g *Gateway) replies(ctx context.Context, id string, count int) ([]Tweet, error) {
	var tweets []Tweet
	err := g.call(ctx, "GET", g.tweetsURL+"/tweets/"+url.PathEscape(id)+"/replies?limit="+strconv.Itoa(count), nil, &tweets)
	return tweets, err
}

type TweetStats struct {
	FavoriteCount int64 `json:"favorite_count"`
	ReplyCount    int64 `json:"reply_count"`
	RetweetCount  int64 `json:"retweet_count"`
	Favorited     bool  `json:"favorited"`
	Retweeted     bool  `json:"retweeted"`
}

func (g *Gateway) stats(ctx context.Context, tweetID, viewerID string) (TweetStats, error) {
	var stats TweetStats
	endpoint := g.tweetsURL + "/tweets/" + url.PathEscape(tweetID) + "/reactions?user_id=" + url.QueryEscape(viewerID)
	err := g.call(ctx, "GET", endpoint, nil, &stats)
	return stats, err
}

func (g *Gateway) favorite(ctx context.Context, tweetID, userID string) error {
	if err := g.checkPostAccess(ctx, tweetID); err != nil {
		return err
	}
	return g.call(ctx, "PUT", g.tweetsURL+"/tweets/"+url.PathEscape(tweetID)+"/reaction", map[string]string{"user_id": userID}, nil)
}

func (g *Gateway) unfavorite(ctx context.Context, tweetID, userID string) error {
	endpoint := g.tweetsURL + "/tweets/" + url.PathEscape(tweetID) + "/reaction?user_id=" + url.QueryEscape(userID)
	return g.call(ctx, "DELETE", endpoint, nil, nil)
}

func (g *Gateway) retweet(ctx context.Context, tweetID, userID string) error {
	if err := g.checkPostAccess(ctx, tweetID); err != nil {
		return err
	}
	return g.call(ctx, "PUT", g.tweetsURL+"/tweets/"+url.PathEscape(tweetID)+"/retweet", map[string]string{"user_id": userID}, nil)
}

func (g *Gateway) unretweet(ctx context.Context, tweetID, userID string) error {
	endpoint := g.tweetsURL + "/tweets/" + url.PathEscape(tweetID) + "/retweet?user_id=" + url.QueryEscape(userID)
	return g.call(ctx, "DELETE", endpoint, nil, nil)
}

func (g *Gateway) likes(ctx context.Context, userID string, count int) ([]Tweet, error) {
	var tweets []Tweet
	endpoint := g.tweetsURL + "/likes?user_id=" + url.QueryEscape(userID) + "&limit=" + strconv.Itoa(count)
	err := g.call(ctx, "GET", endpoint, nil, &tweets)
	return tweets, err
}

func (g *Gateway) reposts(ctx context.Context, userID string, count int) ([]Tweet, error) {
	var tweets []Tweet
	endpoint := g.tweetsURL + "/reposts?user_id=" + url.QueryEscape(userID) + "&limit=" + strconv.Itoa(count)
	err := g.call(ctx, "GET", endpoint, nil, &tweets)
	return tweets, err
}

func (g *Gateway) timeline(ctx context.Context, count int) ([]Tweet, error) {
	var tweets []Tweet
	err := g.call(ctx, "GET", g.tweetsURL+"/timeline?limit="+strconv.Itoa(count), nil, &tweets)
	return tweets, err
}

func writeAPIError(w http.ResponseWriter, err error) {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		common.Error(w, apiErr.Status, apiErr.Message)
		return
	}
	common.Error(w, 500, "internal error")
}

func xDate(t time.Time) string { return t.UTC().Format("Mon Jan 02 15:04:05 -0700 2006") }

func xUser(user User) map[string]any {
	urls := []any{}
	if user.Website != "" {
		urls = append(urls, map[string]any{"expanded_url": user.Website, "display_url": user.Website, "url": user.Website})
	}
	return map[string]any{
		"__typename": "User", "id": user.ID, "rest_id": user.ID, "is_blue_verified": user.Verified,
		"core":                      map[string]any{"screen_name": user.ScreenName, "name": user.Name},
		"privacy":                   map[string]any{"protected": user.Protected},
		"relationship_counts":       map[string]any{"followers": user.Followers, "following": user.Following},
		"relationship_perspectives": map[string]any{"following": user.IsFollowing, "followed_by": user.IsFollowedBy, "blocking": user.Blocking, "blocked_by": user.BlockedBy, "muting": user.Muting, "follow_request_sent": user.FollowRequested},
		"is_following":              user.IsFollowing, "is_followed_by": user.IsFollowedBy,
		"follow_request_sent": user.FollowRequested, "blocking": user.Blocking, "blocked_by": user.BlockedBy, "muting": user.Muting,
		"avatar":   map[string]any{"image_url": user.AvatarURL},
		"location": map[string]any{"location": user.Location},
		"profile_bio": map[string]any{"description": user.Bio,
			"entities": map[string]any{"url": map[string]any{"urls": urls}}},
		"profile_image_shape":         "Circle",
		"verification":                map[string]any{"is_blue_verified": user.Verified, "verified_type": nil},
		"community_memberships_slice": map[string]any{"items_results": []any{}},
		"legacy": map[string]any{
			"screen_name": user.ScreenName, "name": user.Name,
			"description": user.Bio, "profile_image_url_https": user.AvatarURL,
			"location": user.Location, "url": user.Website,
			"created_at": xDate(user.CreatedAt), "verified": user.Verified,
			"followers_count": user.Followers, "friends_count": user.Following,
			"protected": user.Protected, "pinned_tweet_ids_str": user.Pinned, "following": user.IsFollowing, "blocking": user.Blocking, "muting": user.Muting,
		},
	}
}

func (g *Gateway) xTweet(ctx context.Context, tweet Tweet) (map[string]any, error) {
	user, err := g.userByID(ctx, tweet.AuthorID)
	if err != nil {
		return nil, err
	}
	viewerID := ""
	if actor := actorFrom(ctx); actor != nil {
		viewerID = actor.UserID
	}
	stats, err := g.stats(ctx, tweet.ID, viewerID)
	if err != nil {
		return nil, err
	}
	meta := map[string]any{}
	if g.socialURL != "" {
		if err = g.call(ctx, "GET", g.socialURL+"/posts/"+url.PathEscape(tweet.ID)+"/meta?viewer_id="+url.QueryEscape(viewerID), nil, &meta); err != nil {
			return nil, err
		}
	}
	legacy := map[string]any{
		"id_str": tweet.ID, "full_text": tweet.Text,
		"user_id_str": tweet.AuthorID, "conversation_id_str": tweet.ID,
		"created_at":     xDate(tweet.CreatedAt),
		"favorite_count": stats.FavoriteCount, "reply_count": stats.ReplyCount,
		"favorited": stats.Favorited, "retweeted": stats.Retweeted, "retweet_count": stats.RetweetCount,
	}
	if tweet.ReplyToID != nil {
		legacy["in_reply_to_status_id_str"] = *tweet.ReplyToID
		legacy["conversation_id_str"] = *tweet.ReplyToID
	}
	mediaEntities := make([]map[string]any, 0, len(tweet.Media))
	for _, media := range tweet.Media {
		mediaType := "photo"
		if strings.HasPrefix(media.MIME, "video/") {
			mediaType = "video"
		}
		entity := map[string]any{
			"id_str": media.ID, "media_key": "3_" + media.ID, "type": mediaType,
			"media_url_https": media.URL, "ext_alt_text": media.Alt,
			"sizes":         map[string]any{"large": map[string]any{"w": 1024, "h": 768, "resize": "fit"}, "small": map[string]any{"w": 680, "h": 510, "resize": "fit"}},
			"original_info": map[string]any{"width": 1024, "height": 768},
		}
		if mediaType == "video" {
			entity["video_info"] = map[string]any{"aspect_ratio": []int{4, 3}, "variants": []any{map[string]any{"content_type": media.MIME, "url": media.URL}}}
		}
		mediaEntities = append(mediaEntities, entity)
	}
	legacy["extended_entities"] = map[string]any{"media": mediaEntities}
	urls, tags := tweetTextEntities(tweet.Text)
	legacy["entities"] = map[string]any{"media": mediaEntities, "urls": urls, "user_mentions": []any{}, "hashtags": tags}
	result := map[string]any{
		"__typename": "Tweet", "id": tweet.ID, "rest_id": tweet.ID,
		"core": map[string]any{"user_results": map[string]any{"result": xUser(user)}},
		"details": map[string]any{
			"full_text": tweet.Text, "created_at_ms": tweet.CreatedAt.UnixMilli(), "display_text_range": []int{0, textLength(tweet.Text)}, "hashtag_entities": tags, "timestamp_entities": []any{}, "cashtag_entities": []any{}, "smarttags": []any{},
		},
		"counts": map[string]any{
			"favorite_count": stats.FavoriteCount, "reply_count": stats.ReplyCount,
			"retweet_count": stats.RetweetCount, "quote_count": meta["quote_count"], "bookmark_count": meta["bookmark_count"],
		},
		"perspective":     map[string]any{"favorited": stats.Favorited, "retweeted": stats.Retweeted, "bookmarked": meta["bookmarked"]},
		"media_entities2": mediaEntities, "url_entities": urls, "mention_entities": []any{},
		"legacy": legacy,
	}
	legacy["bookmark_count"] = meta["bookmark_count"]
	legacy["bookmarked"] = meta["bookmarked"]
	legacy["quote_count"] = meta["quote_count"]
	result["is_translatable"] = false
	result["is_pinned"] = meta["pinned"]
	if id := socialString(meta, "quoted_tweet_id"); id != "" && ctx.Value(quoteDepthKey{}) == nil {
		if quoted, e := g.tweetByID(ctx, id); e == nil {
			q, e := g.xTweet(context.WithValue(ctx, quoteDepthKey{}, true), quoted)
			if e == nil {
				result["quoted_status_result"] = map[string]any{"result": q}
				result["quoted_tweet_results"] = map[string]any{"result": q, "rest_id": id}
				legacy["is_quote_status"] = true
				legacy["quoted_status_id_str"] = id
			}
		}
	}
	return result, nil
}

func (g *Gateway) instructions(ctx context.Context, tweets []Tweet) ([]map[string]any, error) {
	var err error
	tweets, err = g.visibleTweets(ctx, tweets)
	if err != nil {
		return nil, err
	}
	entries := make([]map[string]any, 0, len(tweets))
	for _, tweet := range tweets {
		result, err := g.xTweet(ctx, tweet)
		if err != nil {
			return nil, err
		}
		sortIndex := tweet.ID
		if len(sortIndex) < 20 {
			sortIndex = strings.Repeat("0", 20-len(sortIndex)) + sortIndex
		}
		tweetResults := map[string]any{"rest_id": tweet.ID, "result": result}
		entries = append(entries, map[string]any{
			"entryId": "tweet-" + tweet.ID, "sortIndex": sortIndex,
			"entry_id": "tweet-" + tweet.ID, "sort_index": sortIndex,
			"content": map[string]any{
				"__typename": "TimelineTimelineItem",
				"content":    map[string]any{"__typename": "TimelineTweet", "tweet_results": tweetResults},
				"entryType":  "TimelineTimelineItem",
				"itemContent": map[string]any{
					"itemType": "TimelineTweet", "tweet_results": tweetResults,
				},
			},
		})
	}
	return []map[string]any{{"type": "TimelineAddEntries", "__typename": "TimelineAddEntries", "entries": entries}}, nil
}

func mustString(vars map[string]any, name string) (string, error) {
	value, ok := vars[name].(string)
	if !ok || strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("variables.%s is required", name)
	}
	return value, nil
}

func count(vars map[string]any) int {
	value, ok := vars["count"].(float64)
	if !ok || value < 1 {
		return 20
	}
	if value > 100 {
		return 100
	}
	return int(value)
}
