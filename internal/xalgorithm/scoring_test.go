package xalgorithm

import (
	"math"
	"testing"
)

func TestPublishedRustHandComputedParity(t *testing.T) {
	yes, no := true, false
	w := Weights{Heads: map[string]float64{"favorite": 2, "reply": 4, "retweet": 1, "dwell": 0.5, "vqv": 3, "dwell_time": 0.1, "click_dwell_time": 0.2, "post_unexplored": 1.5, "report": -100, "not_interested": -10}, MutualReplyBoost: 1, MutualDwellBoost: 0.5, AuthorDiversity: true, DiversityDecay: 0.5, DiversityFloor: 0.25, RescoreNetworkReplies: true}
	c := []Candidate{
		{AuthorID: "1", InNetwork: &yes, MutualFollow: true, Heads: map[string]float64{"favorite": 0.5, "reply": 0.1, "dwell": 0.4, "dwell_time": 10, "post_unexplored": 0.2}},
		{AuthorID: "2", InNetwork: &no, Heads: map[string]float64{"favorite": 0.2, "vqv": 0.9, "click_dwell_time": 5, "post_unexplored": 0.2}},
		{AuthorID: "1", InNetwork: &yes, VQVEligible: true, Heads: map[string]float64{"favorite": 0.05, "vqv": 0.5, "click_dwell_time": 5}},
		{AuthorID: "3", InNetwork: &yes, Reply: true, MutualFollow: true, Heads: map[string]float64{"favorite": 0.01, "report": 0.05}},
	}
	weighted, scores := ValueScores(w, 0.75, c)
	wantWeighted := []float64{3.201, 1.401, 2.601, (0.02 - 5 + 110) / 122 * 0.001}
	wantScores := []float64{3.201, 1.401 * 0.75, 2.601 * 0.625, wantWeighted[3] * 0.75}
	for i := range c {
		if math.Abs(weighted[i]-wantWeighted[i]) > 1e-12 || math.Abs(scores[i]-wantScores[i]) > 1e-12 {
			t.Fatalf("upstream parity %d: weighted=%g score=%g", i, weighted[i], scores[i])
		}
	}
	for _, score := range []float64{-3, 0, 4} {
		if math.Abs(UnoffsetScore(OffsetScore(score, w), w)-score) > 1e-10 {
			t.Fatal("offset inverse", score)
		}
	}
}
func TestTopKDuplicatesConversationsAndColdStart(t *testing.T) {
	c := []Candidate{{ID: "1", ConversationID: "root"}, {ID: "2", ConversationID: "root"}, {ID: "2"}, {ID: "3"}}
	order := TopK(c, []float64{1, 4, 3, 2}, 3)
	if len(order) != 2 || order[0] != 1 || order[1] != 3 {
		t.Fatal(order)
	}
	rank := 0
	c[3].ColdStartLift = &rank
	_, scores := ValueScores(Weights{Heads: map[string]float64{}}, 1, []Candidate{{Heads: map[string]float64{}, WeightedScore: floatPtr(2)}, {Heads: map[string]float64{}, WeightedScore: floatPtr(1), ColdStartLift: &rank}})
	if scores[1] != 2 {
		t.Fatal(scores)
	}
}
func floatPtr(n float64) *float64 { return &n }
