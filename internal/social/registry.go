package social

import (
	_ "embed"
	"encoding/json"
)

type Field struct {
	Name       string  `json:"name"`
	Alias      string  `json:"alias"`
	Kind       string  `json:"kind"`
	Type       string  `json:"type"`
	Plural     bool    `json:"plural"`
	Selections []Field `json:"selections"`
}
type Spec struct {
	Name       string   `json:"name"`
	ID         string   `json:"id"`
	Kind       string   `json:"kind"`
	Handler    string   `json:"handler"`
	Action     string   `json:"action"`
	Entity     string   `json:"entity"`
	Web        bool     `json:"web"`
	Roots      []string `json:"roots"`
	Selections []Field  `json:"selections"`
}

//go:embed operations.json
var registryData []byte
var Registry map[string]Spec

func init() {
	var entries []Spec
	if err := json.Unmarshal(registryData, &entries); err != nil {
		panic(err)
	}
	Registry = make(map[string]Spec, len(entries))
	for _, s := range entries {
		Registry[s.Name+":"+s.ID] = s
	}
}
func Lookup(name, id string) (Spec, bool) { s, ok := Registry[name+":"+id]; return s, ok }

type Result struct {
	Value    map[string]any `json:"value"`
	TweetIDs []string       `json:"tweet_ids,omitempty"`
	UserIDs  []string       `json:"user_ids,omitempty"`
	UserID   string         `json:"user_id,omitempty"`
	Timeline bool           `json:"timeline,omitempty"`
	Cursor   string         `json:"cursor,omitempty"`
	Scalar   any            `json:"scalar,omitempty"`
}
