# FlashLarry Go recommendation core

Adapted from [xai-org/x-algorithm](https://github.com/xai-org/x-algorithm),
commit `b79b947ce9283c786822274ef8fe208c33c585b2`, under Apache 2.0.
The original license and Rust scoring sources are preserved in `upstream/`.
This Go translation and the FlashLarry integration were added on 2026-10-01.

`scoring.go` ports the published weighted probability value model, mutual-follow
reply bonuses, negative-score offset/inversion, author diversity, out-of-network
multiplier, pre-offset adjustment, cold-start lift and score-based top-K selection.
The defaults come from `home-mixer/params/param.rs`. Tests include the upstream
`hand_computed_parity` fixture and inverse-offset/selection checks.

`internal/social/feed.go` supplies local candidates, visibility/age filters,
conversation collapse and stable cursor snapshots. FlashLarry estimates action
probabilities from served-post engagement, followed authors and the viewer's
previously served, liked/reposted posts. It applies freshness, repeat-delivery
penalties and administrator priorities after the value-model calculation.
Priority -100..100 maps to a multiplier 2^(priority/25); zero is neutral.

This is a port of the recommendation core, not the entire upstream repository.
Phoenix neural inference/training, embedding services, Thunder infrastructure,
VM-ranker DPP selection and online experimentation are not translated or
deployed. Local estimates are not Phoenix predictions and ranking will differ
from X. The algorithm mode considers up to 500 visible posts from the last
48 hours. The newest-first mode sorts by descending post ID and ignores
priorities; Following is always chronological. A feed cursor is bound to its
viewer, settings revision and ordering snapshot and expires after 15 minutes.
