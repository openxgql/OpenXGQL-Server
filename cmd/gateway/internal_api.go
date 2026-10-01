package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"flashlarry-api/internal/common"
	"flashlarry-api/internal/social"
)

// Query IDs are a snapshot of the develop branch of TwitterInternalAPIDocument.
// The upstream project regenerates them; this server deliberately supports only
// the operations listed here.
var operations = map[string]struct {
	queryID string
	method  string
}{
	"UserByScreenName":     {"KybxDj9RrADIITXlGG8kpw", "GET"},
	"UserByRestId":         {"IdmRdjYxIGI39Hdwkwo5cQ", "GET"},
	"Viewer":               {"9t128XgFic52jPUEkJMf6w", "GET"},
	"TweetResultByRestId":  {"Xl0tsHf4AzflMRjbw9e70A", "GET"},
	"TweetDetail":          {"zoF7_t363wZyzylk-BLfZQ", "GET"},
	"UserTweets":           {"jeAA-59Y9FL7FmjgBNIVPw", "GET"},
	"UserTweetsAndReplies": {"wI-ubAWfScnG6odLK4XgCg", "GET"},
	"UserRepliesTimeline":  {"xz348nziCm96wndJ1S0MUQ", "GET"},
	"Likes":                {"XHn_Tw60c6pi0n3DGhpwiA", "GET"},
	"HomeTimeline":         {"og4a4SdSF3WiQkkwaPCdPg", "GET"},
	"CreateTweet":          {"GYdIGqVWfZNho79bQ2XDoA", "POST"},
	"DeleteTweet":          {"nxpZCY2K-I6QoFHAHeojFQ", "POST"},
	"FavoriteTweet":        {"lI07N6Otwv1PhnEgXILM7A", "POST"},
	"UnfavoriteTweet":      {"ZYKSe-w7KEslx3JhSIk5LA", "POST"},
	"CreateRetweet":        {"mbRO74GrOvSfRcJnlMapnQ", "POST"},
	"DeleteRetweet":        {"ZyZigVsNiFO6v1dEks1eWg", "POST"},
}

// IDs extracted from the public 2026-09-30 X web bundle. The new web client
// uses Relay operation names that differ from the earlier documented API.
var webOperations = map[string]struct {
	queryID string
	method  string
	legacy  string
}{
	"viewerQuery":                       {"MI3lNBwpRLeVYwc3E9Tltw", "GET", "Viewer"},
	"editProfileSlotQuery":              {"RNpVJFms0omUKtvSJ55fhw", "GET", "Viewer"},
	"HomeTimelineQuery":                 {"f0-mVVNN4eMC09LORoEYxA", "GET", "HomeTimeline"},
	"HomeLatestTimelineQuery":           {"7KNrHG1YVaKEeICJxjKgJQ", "GET", "HomeTimeline"},
	"UserByScreenName":                  {"x7x9Bxa527AUOmbWTdr-uQ", "GET", "UserByScreenName"},
	"TweetResultByRestId":               {"3RA1Jr6Z9PuyOL6KdBExyw", "GET", "TweetResultByRestId"},
	"tweetMediaViewerQuery":             {"DDo2a-l7Qi7XjKEiRFTtFQ", "GET", "TweetResultByRestId"},
	"TweetDetail":                       {"WinWGVk6HFkcZLRRhT8A9g", "GET", "TweetDetail"},
	"UserOriginalsTimeline":             {"K_QDRAnlchFNchjURHVl5A", "GET", ""},
	"UserRepostsTimeline":               {"kBHhyqUmkmyFrKNh40VIlg", "GET", ""},
	"UserVideoTimeline":                 {"afjvDJ1fVO2SshlE-1iORw", "GET", ""},
	"SearchByRawQuery":                  {"AtCa2OqMsKfVhX1VzMDmiw", "GET", ""},
	"globalSearchTypeaheadQuery":        {"HOxsynH5lBZ_Q_5K5hixGQ", "GET", ""},
	"followUserMutation":                {"m5VXpPA-MH4DH7epCa7hCg", "POST", ""},
	"unfollowUserMutation":              {"RjGnpAQuMmcqXSjoCR7t6w", "POST", ""},
	"followingQuery":                    {"Q8Gw7jcW4trNQLHsJlLsAA", "GET", ""},
	"followersQuery":                    {"mrr74pec9CI9r9ykmcvTug", "GET", ""},
	"createTweetMutation":               {"SmSboNX6JQsVT4yRLd9JmQ", "POST", "CreateTweet"},
	"deleteTweetMutation":               {"6DqoQrCrai4VWFRVzm9pDg", "POST", "DeleteTweet"},
	"likeButtonFavoriteMutation":        {"kD580ja7oRwK5qf7CFtyNA", "POST", "FavoriteTweet"},
	"likeButtonUnfavoriteMutation":      {"_PheAOZ7yd8APx29WhE-7w", "POST", "UnfavoriteTweet"},
	"intentModalFavoriteMutation":       {"GMT2cdsF3i-a0ZSqMJN1Gg", "POST", "FavoriteTweet"},
	"retweetButtonRetweetMutation":      {"uVUVwxxC4_1q3qQ9H3VDag", "POST", "CreateRetweet"},
	"retweetButtonUnretweetMutation":    {"ELme9SiWUrwfdBWHIdcx1w", "POST", "Unretweet"},
	"intentModalRetweetMutation":        {"LfGx1Gv7LDwwPbQIdvfG1w", "POST", "CreateRetweet"},
	"pinnedTimelinesQuery":              {"DfjAqjIlWC0Bj6aOSGsKoA", "GET", ""},
	"AudienceMenuQuery":                 {"PcoCkb-bVdz8F8n77RvQ2w", "GET", "Viewer"},
	"ConversationControlsSettingsQuery": {"OnXAuQWojOHDXWRDW6tJcg", "GET", ""},
}

func graphqlError(w http.ResponseWriter, status int, message string) {
	common.WriteJSON(w, status, map[string]any{"errors": []map[string]string{{"message": message}}})
}

func requestVariables(w http.ResponseWriter, r *http.Request) (map[string]any, bool) {
	var raw json.RawMessage
	if r.Method == http.MethodGet {
		raw = json.RawMessage(r.URL.Query().Get("variables"))
	} else {
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		var body struct {
			Variables json.RawMessage `json:"variables"`
			QueryID   string          `json:"queryId"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			graphqlError(w, 400, "invalid JSON body")
			return nil, false
		}
		raw = body.Variables
		if body.QueryID != "" && body.QueryID != r.PathValue("queryID") {
			graphqlError(w, 400, "body queryId does not match path")
			return nil, false
		}
	}
	if len(raw) == 0 || string(raw) == "null" {
		return map[string]any{}, true
	}
	if raw[0] == '"' {
		var encoded string
		if err := json.Unmarshal(raw, &encoded); err != nil {
			graphqlError(w, 400, "invalid variables")
			return nil, false
		}
		raw = []byte(encoded)
	}
	var variables map[string]any
	if err := json.Unmarshal(raw, &variables); err != nil || variables == nil {
		graphqlError(w, 400, "variables must be a JSON object")
		return nil, false
	}
	return variables, true
}

func (g *Gateway) internalGraphQL(w http.ResponseWriter, r *http.Request) {
	operation := r.PathValue("operation")
	spec, ok := operations[operation]
	webSpec, webOK := webOperations[operation]
	isWeb := webOK && webSpec.queryID == r.PathValue("queryID")
	if full, found := social.Lookup(operation, r.PathValue("queryID")); found && (full.Handler != "core" || (!isWeb && (!ok || spec.queryID != r.PathValue("queryID")))) {
		g.registryGraphQL(w, r, full)
		return
	}
	if !isWeb && (!ok || spec.queryID != r.PathValue("queryID")) {
		graphqlError(w, 404, "unsupported operation or query ID")
		return
	}
	method := spec.method
	if isWeb {
		method = webSpec.method
	}
	if r.Method != method && !(method == http.MethodGet && r.Method == http.MethodPost) {
		if method == http.MethodGet {
			w.Header().Set("Allow", "GET, POST")
		} else {
			w.Header().Set("Allow", method)
		}
		graphqlError(w, 405, "method not allowed")
		return
	}
	vars, ok := requestVariables(w, r)
	if !ok {
		return
	}
	if isWeb {
		switch operation {
		case "UserOriginalsTimeline":
			g.webUserOriginalsTimeline(w, r, vars)
			return
		case "UserRepostsTimeline":
			g.webUserRepostsTimeline(w, r, vars)
			return
		case "UserVideoTimeline":
			g.webUserVideoTimeline(w, r, vars)
			return
		case "SearchByRawQuery":
			g.webSearchByRawQuery(w, r, vars)
			return
		case "globalSearchTypeaheadQuery":
			g.webSearchTypeahead(w, r, vars)
			return
		case "followUserMutation":
			g.webFollowMutation(w, r, vars, true)
			return
		case "unfollowUserMutation":
			g.webFollowMutation(w, r, vars, false)
			return
		case "followingQuery":
			g.webFollowList(w, r, vars, "following")
			return
		case "followersQuery":
			g.webFollowList(w, r, vars, "followers")
			return
		case "pinnedTimelinesQuery":
			common.WriteJSON(w, 200, map[string]any{"data": map[string]any{"pinned_timelines": map[string]any{"pinned_timelines": []any{}}}})
			return
		case "ConversationControlsSettingsQuery":
			common.WriteJSON(w, 200, map[string]any{"data": map[string]any{"get_conversation_control_settings": map[string]any{"settings": []any{}}, "viewer_v2": map[string]any{"is_active_creator": false}}})
			return
		}
	}
	execute := operation
	if isWeb {
		execute = webSpec.legacy
		switch operation {
		case "UserByScreenName":
			vars["screen_name"] = vars["screenName"]
		case "TweetResultByRestId", "tweetMediaViewerQuery":
			vars["tweetId"] = vars["restId"]
		case "likeButtonFavoriteMutation", "likeButtonUnfavoriteMutation", "intentModalFavoriteMutation":
			vars["tweet_id"] = vars["tweetId"]
		case "retweetButtonRetweetMutation", "retweetButtonUnretweetMutation", "intentModalRetweetMutation":
			vars["tweet_id"] = vars["tweetId"]
		}
	}
	result, err := g.executeInternal(r, execute, vars)
	if err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) {
			graphqlError(w, apiErr.Status, apiErr.Message)
		} else {
			graphqlError(w, 400, err.Error())
		}
		return
	}
	if isWeb {
		switch operation {
		case "AudienceMenuQuery":
			result = map[string]any{"viewer_v2": result["viewer"]}
		case "UserByScreenName":
			result = map[string]any{"user_result_by_screen_name": result["user"]}
		case "TweetResultByRestId", "tweetMediaViewerQuery":
			if tweetResult, ok := result["tweetResult"].(map[string]any); ok {
				if tweet, ok := tweetResult["result"].(map[string]any); ok {
					tweetResult["rest_id"] = tweet["rest_id"]
				}
			}
			result = map[string]any{"tweet_result_by_rest_id": result["tweetResult"]}
		case "HomeTimelineQuery", "HomeLatestTimelineQuery":
			if home, ok := result["home"].(map[string]any); ok {
				home["id"] = "flashlarry-home"
				if operation == "HomeTimelineQuery" {
					result = map[string]any{"home_timeline": home}
				}
			}
		}
	}
	common.WriteJSON(w, 200, map[string]any{"data": result})
}

func (g *Gateway) webUserOriginalsTimeline(w http.ResponseWriter, r *http.Request, vars map[string]any) {
	screenName, err := mustString(vars, "screenName")
	if err != nil {
		graphqlError(w, 400, err.Error())
		return
	}
	user, err := g.userByName(r.Context(), screenName)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	tweets, err := g.tweetsByUser(r.Context(), user.ID, count(vars), "posts")
	if err != nil {
		writeAPIError(w, err)
		return
	}
	instructions, err := g.instructions(r.Context(), tweets)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	timelineID := "user-" + user.ID + "-originals"
	common.WriteJSON(w, 200, map[string]any{"data": map[string]any{
		"user_result_by_screen_name": map[string]any{"result": map[string]any{
			"__typename": "User", "rest_id": user.ID,
			"profile_user_originals_timeline": map[string]any{"id": timelineID,
				"timeline": map[string]any{"id": timelineID, "instructions": instructions}},
		}},
	}})
}

func (g *Gateway) webUserRepostsTimeline(w http.ResponseWriter, r *http.Request, vars map[string]any) {
	screenName, err := mustString(vars, "screenName")
	if err != nil {
		graphqlError(w, 400, err.Error())
		return
	}
	user, err := g.userByName(r.Context(), screenName)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	tweets, err := g.reposts(r.Context(), user.ID, count(vars))
	if err != nil {
		writeAPIError(w, err)
		return
	}
	instructions, err := g.instructions(r.Context(), tweets)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	timelineID := "user-" + user.ID + "-reposts"
	common.WriteJSON(w, 200, map[string]any{"data": map[string]any{
		"user_result_by_screen_name": map[string]any{"result": map[string]any{
			"__typename": "User", "rest_id": user.ID,
			"profile_user_reposts_timeline": map[string]any{"id": timelineID, "timeline": map[string]any{"id": timelineID, "instructions": instructions}},
		}},
	}})
}

func (g *Gateway) webUserVideoTimeline(w http.ResponseWriter, r *http.Request, vars map[string]any) {
	screenName, err := mustString(vars, "screenName")
	if err != nil {
		graphqlError(w, 400, err.Error())
		return
	}
	user, err := g.userByName(r.Context(), screenName)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	var tweets []Tweet
	endpoint := g.tweetsURL + "/videos?user_id=" + url.QueryEscape(user.ID) + "&limit=" + strconv.Itoa(count(vars))
	if err := g.call(r.Context(), "GET", endpoint, nil, &tweets); err != nil {
		writeAPIError(w, err)
		return
	}
	instructions, err := g.instructions(r.Context(), tweets)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	timelineID := "user-" + user.ID + "-videos"
	common.WriteJSON(w, 200, map[string]any{"data": map[string]any{
		"user_result_by_screen_name": map[string]any{"result": map[string]any{
			"__typename": "User", "rest_id": user.ID,
			"profile_user_video_timeline": map[string]any{"id": timelineID, "timeline": map[string]any{"id": timelineID, "instructions": instructions}},
		}},
	}})
}

func (g *Gateway) executeInternal(r *http.Request, operation string, vars map[string]any) (map[string]any, error) {
	ctx := r.Context()
	switch operation {
	case "UserByScreenName":
		name, err := mustString(vars, "screen_name")
		if err != nil {
			return nil, err
		}
		user, err := g.userByName(ctx, name)
		if err != nil {
			return nil, err
		}
		return map[string]any{"user": map[string]any{"result": xUser(user)}}, nil
	case "UserByRestId":
		id, err := mustString(vars, "userId")
		if err != nil {
			return nil, err
		}
		user, err := g.userByID(ctx, id)
		if err != nil {
			return nil, err
		}
		return map[string]any{"user": map[string]any{"result": xUser(user)}}, nil
	case "Viewer":
		user, err := g.userByID(ctx, actorFrom(ctx).UserID)
		if err != nil {
			return nil, err
		}
		return map[string]any{"viewer": map[string]any{"is_active_creator": false, "claims": []any{}, "user_results": map[string]any{"result": xUser(user)}}}, nil
	case "TweetResultByRestId", "TweetDetail":
		field := "tweetId"
		if operation == "TweetDetail" {
			field = "focalTweetId"
		}
		id, err := mustString(vars, field)
		if err != nil {
			return nil, err
		}
		tweet, err := g.tweetByID(ctx, id)
		if err != nil {
			return nil, err
		}
		if operation == "TweetResultByRestId" {
			result, err := g.xTweet(ctx, tweet)
			if err != nil {
				return nil, err
			}
			return map[string]any{"tweetResult": map[string]any{"result": result}}, nil
		}
		replies, err := g.replies(ctx, id, count(vars))
		if err != nil {
			return nil, err
		}
		result, err := g.xTweet(ctx, tweet)
		if err != nil {
			return nil, err
		}
		tweets := append([]Tweet{tweet}, replies...)
		instructions, err := g.instructions(ctx, tweets)
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"tweet_result_by_rest_id":                  map[string]any{"rest_id": id, "result": result},
			"threaded_conversation_with_injections_v2": map[string]any{"instructions": instructions},
		}, nil
	case "UserTweets", "UserTweetsAndReplies", "UserRepliesTimeline", "Likes":
		id, err := mustString(vars, "userId")
		if err != nil {
			return nil, err
		}
		user, err := g.userByID(ctx, id)
		if err != nil {
			return nil, err
		}
		var tweets []Tweet
		if operation == "Likes" {
			tweets, err = g.likes(ctx, id, count(vars))
		} else {
			mode := "posts"
			if operation == "UserTweetsAndReplies" {
				mode = "all"
			}
			if operation == "UserRepliesTimeline" {
				mode = "replies"
			}
			tweets, err = g.tweetsByUser(ctx, id, count(vars), mode)
		}
		if err != nil {
			return nil, err
		}
		instructions, err := g.instructions(ctx, tweets)
		if err != nil {
			return nil, err
		}
		return map[string]any{"user": map[string]any{"result": map[string]any{
			"rest_id": user.ID, "timeline_v2": map[string]any{"timeline": map[string]any{"instructions": instructions}},
		}}}, nil
	case "HomeTimeline":
		instructions, err := g.homeFeed(r, vars)
		if err != nil {
			return nil, err
		}
		return map[string]any{"home": map[string]any{"home_timeline_urt": map[string]any{"instructions": instructions}}}, nil
	case "CreateTweet":
		actorID := actorFrom(ctx).UserID
		vars = normalizedVariables(vars)
		if g.socialURL != "" {
			if err := g.call(ctx, "POST", g.socialURL+"/posts/validate", map[string]any{"user_id": actorID, "variables": vars}, nil); err != nil {
				return nil, err
			}
		}

		text, ok := vars["tweet_text"].(string)
		if !ok {
			return nil, fmt.Errorf("variables.tweet_text is required")
		}
		mediaIDs := []string{}
		if media, ok := vars["media"].(map[string]any); ok {
			if entities, ok := media["media_entities"].([]any); ok {
				for _, entity := range entities {
					object, ok := entity.(map[string]any)
					if !ok {
						return nil, fmt.Errorf("invalid media entity")
					}
					id, err := mustString(object, "media_id")
					if err != nil {
						return nil, err
					}
					mediaIDs = append(mediaIDs, id)
				}
			}
		}
		var replyTo *string
		if reply, exists := vars["reply"]; exists {
			object, ok := reply.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("variables.reply must be an object")
			}
			id, err := mustString(object, "in_reply_to_tweet_id")
			if err != nil {
				return nil, err
			}
			replyTo = &id
		}
		tweet, err := g.createTweet(ctx, actorID, text, replyTo, mediaIDs...)
		if err != nil {
			return nil, err
		}
		if g.socialURL != "" {
			if err := g.call(ctx, "POST", g.socialURL+"/posts/commit", map[string]any{"user_id": actorID, "id": tweet.ID, "variables": vars}, nil); err != nil {
				return nil, err
			}
		}

		result, err := g.xTweet(ctx, tweet)
		if err != nil {
			return nil, err
		}
		return map[string]any{"create_tweet": map[string]any{"tweet_results": map[string]any{"rest_id": tweet.ID, "result": result}}}, nil
	case "DeleteTweet":
		actorID := actorFrom(ctx).UserID
		id, err := mustString(vars, "tweet_id")
		if err != nil {
			return nil, err
		}
		deleted, err := g.deleteTweet(ctx, actorID, id)
		if err != nil {
			return nil, err
		}
		return map[string]any{"delete_tweet": map[string]any{"deleted": deleted, "rest_id": id}}, nil
	case "FavoriteTweet", "UnfavoriteTweet":
		id, err := mustString(vars, "tweet_id")
		if err != nil {
			return nil, err
		}
		if operation == "FavoriteTweet" {
			err = g.favorite(ctx, id, actorFrom(ctx).UserID)
			if err != nil {
				return nil, err
			}
			return map[string]any{"favorite_tweet": "Done"}, nil
		}
		err = g.unfavorite(ctx, id, actorFrom(ctx).UserID)
		if err != nil {
			return nil, err
		}
		return map[string]any{"unfavorite_tweet": "Done"}, nil
	case "CreateRetweet", "Unretweet", "DeleteRetweet":
		id, err := mustString(vars, "tweet_id")
		if err != nil && operation == "DeleteRetweet" {
			id, err = mustString(vars, "source_tweet_id")
		}
		if err != nil {
			return nil, err
		}
		if operation == "CreateRetweet" {
			err = g.retweet(ctx, id, actorFrom(ctx).UserID)
			if err != nil {
				return nil, err
			}
			return map[string]any{"create_retweet": map[string]any{"retweet_results": map[string]any{"result": map[string]any{"__typename": "Tweet", "id": id, "rest_id": id}}}}, nil
		}
		err = g.unretweet(ctx, id, actorFrom(ctx).UserID)
		if err != nil {
			return nil, err
		}
		response := map[string]any{"source_tweet_results": map[string]any{"result": map[string]any{"__typename": "Tweet", "id": id, "rest_id": id}}}
		if operation == "DeleteRetweet" {
			return map[string]any{"delete_retweet": response}, nil
		}
		return map[string]any{"unretweet": response}, nil
	}
	return nil, fmt.Errorf("unsupported operation")
}
