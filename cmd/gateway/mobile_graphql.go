package main

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"flashlarry-api/internal/common"
	"flashlarry-api/internal/social"
)

// Extracted from name() and a() in the supplied X 12.25.2 Android APK.
var mobileOperations = map[string]struct {
	id     string
	method string
}{
	"GetUserByScreenNameQuery":           {"ahc6Y1tbn8GXcYebro0mUQ", "GET"},
	"GetUserQuery":                       {"QOythMa1vRjZftfDRQUZ5Q", "GET"},
	"ViewerUserQuery":                    {"yfFb4WTH5goThjKEgmsTAg", "GET"},
	"HomeTimeline":                       {"NuU6hrBJT5vwI8gARTD40w", "GET"},
	"HomeTimelineLatest":                 {"LK4Ouww7JKjgtdpp0LS5Ug", "GET"},
	"ConversationTimeline":               {"uPEbfDet6uViG7z_u3j73w", "GET"},
	"UserWithProfileTweetsQueryV2":       {"q-QwTZ6v6ev_HieIFc0y1g", "GET"},
	"UserProfileOriginalsTimelineQuery":  {"kiFw-e_O1IToRbqw5mo77Q", "GET"},
	"UserProfileRepliesTimelineQuery":    {"iBxNWqJh_FtbrspTvivqIg", "GET"},
	"UserProfileNewRepliesTimelineQuery": {"qIqG47chMgINMqyIU1nlJw", "GET"},
	"UserProfileFavoritesTimelineQuery":  {"Jq1BGgZygANU86gk6akQ4A", "GET"},
	"FavoriteMutation":                   {"2hbLv7zarSqzkOQX24htlw", "POST"},
	"UnfavoriteMutation":                 {"oMLqPFjJe7lMOCnqc2rNoA", "POST"},
	"CreateRepostMutation":               {"-xPzT3t5JtcohA31EUrZgA", "POST"},
	"DeleteRepostMutation":               {"w1Bo2Whh4f4lha5Djgnpvg", "POST"},
	"CreatePost":                         {"JHWaIGS_91fvu3wy6Pi43w", "POST"},
	"DeletePostMutation":                 {"1EVIme6zMCgTO7F95wuElA", "POST"},
}

func (g *Gateway) requiredMobileAuth(next http.Handler) http.Handler {
	protected := g.requiredAuth(next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if cookie, err := r.Cookie("auth_token"); err == nil && len(cookie.Value) == 64 {
			r = r.Clone(r.Context())
			r.Header.Set("Authorization", "Bearer "+cookie.Value)
		}
		protected.ServeHTTP(w, r)
	})
}

func (g *Gateway) mobileGraphQL(w http.ResponseWriter, r *http.Request) {
	op := r.PathValue("operation")
	spec, ok := mobileOperations[op]
	if !ok || r.PathValue("queryID") != spec.id {
		if full, found := social.Lookup(op, r.PathValue("queryID")); found {
			g.registryGraphQL(w, r, full)
			return
		}
		graphqlError(w, 404, "unsupported mobile operation or query ID")
		return
	}
	if r.Method != spec.method {
		w.Header().Set("Allow", spec.method)
		graphqlError(w, 405, "method not allowed")
		return
	}
	vars, ok := requestVariables(w, r)
	if !ok {
		return
	}
	result, err := g.executeMobile(r, op, vars)
	if err != nil {
		if apiErr, ok := err.(*APIError); ok {
			graphqlError(w, apiErr.Status, apiErr.Message)
		} else {
			graphqlError(w, 400, err.Error())
		}
		return
	}
	common.WriteJSON(w, 200, map[string]any{"data": result})
}

func (g *Gateway) executeMobile(r *http.Request, op string, vars map[string]any) (map[string]any, error) {
	ctx := r.Context()
	switch op {
	case "GetUserByScreenNameQuery":
		name, err := mustString(vars, "screen_name")
		if err != nil {
			return nil, err
		}
		user, err := g.userByName(ctx, name)
		if err != nil {
			return nil, err
		}
		return map[string]any{"user_result": map[string]any{"result": xUser(user)}}, nil
	case "GetUserQuery":
		id, err := mustString(vars, "rest_id")
		if err != nil {
			return nil, err
		}
		user, err := g.userByID(ctx, id)
		if err != nil {
			return nil, err
		}
		return map[string]any{"user_result": map[string]any{"result": xUser(user)}}, nil
	case "ViewerUserQuery":
		user, err := g.userByID(ctx, actorFrom(ctx).UserID)
		if err != nil {
			return nil, err
		}
		return map[string]any{"viewer_v2": map[string]any{
			"user_results":      map[string]any{"result": xUser(user)},
			"is_active_creator": false, "super_followers_count": 0,
		}}, nil
	case "HomeTimeline", "HomeTimelineLatest":
		instructions, err := g.homeFeed(r, vars)
		if err != nil {
			return nil, err
		}
		return map[string]any{"timeline_response": map[string]any{
			"timeline": map[string]any{"instructions": instructions},
		}}, nil
	case "ConversationTimeline":
		id, err := mustString(vars, "postId")
		if err != nil {
			return nil, err
		}
		post, err := g.tweetByID(ctx, id)
		if err != nil {
			return nil, err
		}
		replies, err := g.replies(ctx, id, count(vars))
		if err != nil {
			return nil, err
		}
		instructions, err := g.instructions(ctx, append([]Tweet{post}, replies...))
		if err != nil {
			return nil, err
		}
		return map[string]any{"timelineResponse": map[string]any{"instructions": instructions}}, nil
	case "UserWithProfileTweetsQueryV2", "UserProfileOriginalsTimelineQuery", "UserProfileRepliesTimelineQuery", "UserProfileNewRepliesTimelineQuery", "UserProfileFavoritesTimelineQuery":
		id, err := mustString(vars, "rest_id")
		if err != nil {
			return nil, err
		}
		user, err := g.userByID(ctx, id)
		if err != nil {
			return nil, err
		}
		var tweets []Tweet
		if op == "UserProfileFavoritesTimelineQuery" {
			tweets, err = g.likes(ctx, id, count(vars))
		} else {
			mode := "posts"
			if op == "UserWithProfileTweetsQueryV2" {
				mode = "all"
			}
			if strings.Contains(op, "Replies") {
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
		return map[string]any{"user_result": map[string]any{"result": map[string]any{
			"__typename": "User", "rest_id": user.ID,
			"timeline_response": map[string]any{"timeline": map[string]any{"instructions": instructions}},
		}}}, nil
	case "CreatePost":
		body, ok := vars["post_text"].(string)
		if !ok {
			return nil, fmt.Errorf("variables.post_text is required")
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
		var parent *string
		if value, exists := vars["reply"]; exists && value != nil {
			object, ok := value.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("variables.reply must be an object")
			}
			id, err := mustString(object, "in_reply_to_tweet_id")
			if err != nil {
				return nil, err
			}
			parent = &id
		}
		tweet, err := g.createTweet(ctx, actorFrom(ctx).UserID, body, parent, mediaIDs...)
		if err != nil {
			return nil, err
		}
		result, err := g.xTweet(ctx, tweet)
		if err != nil {
			return nil, err
		}
		return map[string]any{"create_tweet": map[string]any{"tweet_results": map[string]any{"result": result}}}, nil
	case "DeletePostMutation":
		id, err := mustString(vars, "tweet_id")
		if err != nil {
			return nil, err
		}
		_, err = g.deleteTweet(ctx, actorFrom(ctx).UserID, id)
		if err != nil {
			return nil, err
		}
		return map[string]any{"delete_tweet": map[string]any{"__typename": "DeleteTweetResponse"}}, nil
	case "FavoriteMutation", "UnfavoriteMutation":
		id, err := mustString(vars, "tweet_id")
		if err != nil {
			return nil, err
		}
		if op == "FavoriteMutation" {
			if err := g.favorite(ctx, id, actorFrom(ctx).UserID); err != nil {
				return nil, err
			}
			return map[string]any{"favorite_tweet": "Done"}, nil
		}
		if err := g.unfavorite(ctx, id, actorFrom(ctx).UserID); err != nil {
			return nil, err
		}
		return map[string]any{"unfavorite_tweet": "Done"}, nil
	case "CreateRepostMutation", "DeleteRepostMutation":
		id, err := mustString(vars, "tweet_id")
		if err != nil {
			return nil, err
		}
		if op == "CreateRepostMutation" {
			if err := g.retweet(ctx, id, actorFrom(ctx).UserID); err != nil {
				return nil, err
			}
			return map[string]any{"create_retweet": map[string]any{"retweet_results": map[string]any{"id": id}}}, nil
		}
		if err := g.unretweet(ctx, id, actorFrom(ctx).UserID); err != nil {
			return nil, err
		}
		return map[string]any{"unretweet": map[string]any{"__typename": "UnretweetResponse"}}, nil
	}
	return nil, fmt.Errorf("unsupported mobile operation")
}

func (g *Gateway) mobileUpdateProfile(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if err := r.ParseForm(); err != nil {
		common.Error(w, 400, "invalid profile form")
		return
	}
	update := map[string]any{}
	for formKey, field := range map[string]string{
		"screen_name": "screen_name", "name": "name", "description": "bio", "location": "location", "url": "website", "profile_image_url_https": "avatar_url",
	} {
		if values, exists := r.PostForm[formKey]; exists && len(values) > 0 {
			update[field] = values[0]
		}
	}
	if len(update) == 0 {
		common.Error(w, 400, "no supported profile fields")
		return
	}
	var user User
	err := g.call(r.Context(), "PATCH", g.usersURL+"/users/"+url.PathEscape(actorFrom(r.Context()).UserID), update, &user)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	common.WriteJSON(w, 200, map[string]any{
		"id_str": user.ID, "screen_name": user.ScreenName, "name": user.Name,
		"description": user.Bio, "location": user.Location, "url": user.Website,
		"profile_image_url_https": user.AvatarURL, "verified": user.Verified,
	})
}

func (g *Gateway) updateProfileImage(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	if err := r.ParseForm(); err != nil {
		common.Error(w, 400, "invalid avatar form")
		return
	}
	mediaID := r.PostForm.Get("media_id")
	if id, err := strconv.ParseInt(mediaID, 10, 64); err != nil || id <= 0 {
		common.Error(w, 400, "valid media_id is required")
		return
	}
	actorID := actorFrom(r.Context()).UserID
	var media struct {
		URL string `json:"url"`
	}
	endpoint := g.tweetsURL + "/media/" + url.PathEscape(mediaID) + "/avatar-info?owner_id=" + url.QueryEscape(actorID)
	if err := g.call(r.Context(), http.MethodGet, endpoint, nil, &media); err != nil {
		writeAPIError(w, err)
		return
	}
	var user User
	if err := g.call(r.Context(), http.MethodPatch, g.usersURL+"/users/"+url.PathEscape(actorID), map[string]string{"avatar_url": media.URL}, &user); err != nil {
		writeAPIError(w, err)
		return
	}
	common.WriteJSON(w, 200, map[string]any{
		"id_str": user.ID, "screen_name": user.ScreenName, "name": user.Name,
		"profile_image_url_https": user.AvatarURL, "profile_banner_url": "",
	})
}
