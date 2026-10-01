// Go port of xai-org/x-algorithm xai-value-model/scoring.rs and weights.rs.
// Upstream b79b947ce9283c786822274ef8fe208c33c585b2, Apache-2.0.
// Changes: Go data structures; deterministic tie ordering and finite-input validation.
package xalgorithm

import (
	"math"
	"sort"
)

const NegativeScoresOffset = 0.001

var positiveHeads = []string{"favorite", "reply", "retweet", "photo_expand", "video_open", "click", "open_link", "profile_click", "vqv", "share", "share_via_dm", "share_via_copy_link", "dwell", "quote", "quoted_click", "quoted_vqv", "follow_author", "post_unexplored"}
var negativeHeads = []string{"not_interested", "block_author", "mute_author", "report", "not_dwelled"}

type Weights struct {
	Heads                              map[string]float64
	MutualReplyBoost, MutualDwellBoost float64
	AuthorDiversity                    bool
	DiversityDecay, DiversityFloor     float64
	RescoreNetworkReplies              bool
	MultiplierPreOffset                bool
}
type Candidate struct {
	ID, AuthorID, ConversationID                                 string
	Heads                                                        map[string]float64
	InNetwork                                                    *bool
	Reply, Retweet, MutualFollow, VQVEligible, QuotedVQVEligible bool
	WeightedScore                                                *float64
	ColdStartLift                                                *int
}

func DefaultWeights() Weights {
	return Weights{Heads: map[string]float64{
		"favorite": 0.5, "reply": 5, "retweet": 1, "photo_expand": 0.05, "video_open": 0.07, "click": 0.3, "open_link": 0.2, "profile_click": 0, "vqv": 0, "share": 2, "share_via_dm": 5, "share_via_copy_link": 20, "dwell": 0.05, "quote": 5, "quoted_click": 0.05, "quoted_vqv": 0, "follow_author": 4, "post_unexplored": 0.02, "not_interested": -47.52, "block_author": -31.2, "mute_author": -58.8, "report": -234, "not_dwelled": -0.02, "dwell_time": 0.004, "click_dwell_time": 0.4}, MutualReplyBoost: 15, DiversityDecay: 1, DiversityFloor: 1}
}
func (w Weights) NegativeSum() float64 {
	s := 0.0
	for _, h := range negativeHeads {
		s -= w.Heads[h]
	}
	return s
}
func (w Weights) TotalSum() float64 {
	s := w.NegativeSum()
	for _, h := range positiveHeads {
		s += w.Heads[h]
	}
	return s
}
func WeightedScore(w Weights, c Candidate) float64 {
	s := 0.0
	keys := make([]string, 0, len(w.Heads))
	for h := range w.Heads {
		keys = append(keys, h)
	}
	sort.Strings(keys)
	for _, h := range keys {
		weight := w.Heads[h]
		if h == "vqv" && !c.VQVEligible || h == "quoted_vqv" && !c.QuotedVQVEligible || h == "post_unexplored" && (c.InNetwork == nil || !*c.InNetwork) {
			continue
		}
		if c.MutualFollow && !c.Reply && !c.Retweet {
			if h == "reply" {
				weight += w.MutualReplyBoost
			}
			if h == "dwell" {
				weight += w.MutualDwellBoost
			}
		}
		head := c.Heads[h]
		if math.IsNaN(head) || math.IsInf(head, 0) {
			head = 0
		}
		s += head * weight
	}
	return s
}
func OffsetScore(score float64, w Weights) float64 {
	total := w.TotalSum()
	if total == 0 {
		return math.Max(score, 0)
	}
	if score < 0 {
		return (score + w.NegativeSum()) / total * NegativeScoresOffset
	}
	return score + NegativeScoresOffset
}
func UnoffsetScore(score float64, w Weights) float64 {
	total := w.TotalSum()
	if total == 0 {
		return score
	}
	if score < NegativeScoresOffset {
		return score/NegativeScoresOffset*total - w.NegativeSum()
	}
	return score - NegativeScoresOffset
}
func DiversityMultiplier(decay, floor, exponent float64) float64 {
	return (1-floor)*math.Pow(decay, exponent) + floor
}
func authorCounts(c []Candidate, scores []float64) []int {
	order := make([]int, len(c))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(i, j int) bool { return scores[order[i]] > scores[order[j]] })
	count := map[string]int{}
	out := make([]int, len(c))
	for _, i := range order {
		out[i] = count[c[i].AuthorID]
		count[c[i].AuthorID]++
	}
	return out
}
func multipliers(w Weights, oon float64, c []Candidate, scores []float64) []float64 {
	count := authorCounts(c, scores)
	out := make([]float64, len(c))
	for i, item := range c {
		m := 1.0
		if w.AuthorDiversity {
			m = DiversityMultiplier(w.DiversityDecay, w.DiversityFloor, float64(count[i]))
		}
		if item.InNetwork != nil && (!*item.InNetwork || w.RescoreNetworkReplies && (item.Reply || item.Retweet)) {
			m *= oon
		}
		out[i] = m
	}
	return out
}
func coldStart(scores []float64, c []Candidate) []float64 {
	out := append([]float64{}, scores...)
	for i, item := range c {
		if item.ColdStartLift == nil {
			continue
		}
		rank := *item.ColdStartLift
		if rank >= 0 && rank < len(scores) {
			ordered := append([]float64{}, scores...)
			sort.Sort(sort.Reverse(sort.Float64Slice(ordered)))
			out[i] = math.Max(out[i], ordered[rank])
		}
		break
	}
	return out
}
func ValueScores(w Weights, oon float64, c []Candidate) ([]float64, []float64) {
	weighted := make([]float64, len(c))
	for i, item := range c {
		if item.WeightedScore != nil {
			weighted[i] = *item.WeightedScore
		} else {
			weighted[i] = OffsetScore(WeightedScore(w, item), w)
		}
	}
	if w.MultiplierPreOffset {
		m := multipliers(w, oon, c, weighted)
		scaled := make([]float64, len(c))
		for i, s := range weighted {
			net := UnoffsetScore(s, w)
			if net >= 0 {
				net *= m[i]
			}
			scaled[i] = OffsetScore(net, w)
		}
		return weighted, coldStart(scaled, c)
	}
	adjusted := coldStart(weighted, c)
	m := multipliers(w, oon, c, adjusted)
	scores := make([]float64, len(c))
	for i, s := range adjusted {
		scores[i] = s * m[i]
	}
	return weighted, scores
}

// TopK includes the upstream duplicate and conversation-collapse filters.
func TopK(candidates []Candidate, scores []float64, k int) []int {
	order := make([]int, len(candidates))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(i, j int) bool { return scores[order[i]] > scores[order[j]] })
	ids, conversations := map[string]bool{}, map[string]bool{}
	out := []int{}
	for _, i := range order {
		c := candidates[i]
		if ids[c.ID] || c.ConversationID != "" && conversations[c.ConversationID] {
			continue
		}
		ids[c.ID] = true
		if c.ConversationID != "" {
			conversations[c.ConversationID] = true
		}
		out = append(out, i)
		if len(out) == k {
			break
		}
	}
	return out
}
