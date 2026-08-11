package core

import (
	"cmp"
	"slices"
	"time"
)

// Post is one remark in a lane: traffic nobody must answer, which is exactly
// what distinguishes it from an Announcement (no Required, no Acked, no
// retries). It is kept so it can be READ later, not so it can be enforced.
//
// Posts used to be stored nowhere. lane_post appended the text to an event and
// returned a serial, and that event was the only copy, so an agent that was
// not polling at that moment never saw it, a restart lost it, and lane_read,
// the tool whose whole job is "read the lane", did not return posts at all. It
// looked like it worked only because the event reached everybody, including
// agents with no business receiving it.
type Post struct {
	Serial uint64
	From   string // the lane that called lane_post
	// OnBehalfOf is the membership holder the post is attributed to. A
	// subagent's traffic is its parent's traffic (§8.2), so peers see one
	// participant rather than a crowd; From stays so the detail is not lost.
	OnBehalfOf string
	Body       string
	At         time.Time
}

// PostHistory returns the most recent posts, oldest first, capped at limit.
func (s *State) PostHistory(ch *Channel, limit int) []Result {
	if limit <= 0 {
		limit = 50
	}
	posts := ch.Posts
	if len(posts) > limit {
		posts = posts[len(posts)-limit:]
	}
	out := make([]Result, 0, len(posts))
	for _, p := range posts {
		r := Result{"serial": p.Serial, "from": p.OnBehalfOf, "body": p.Body, "at": p.At}
		if p.From != p.OnBehalfOf {
			r["sent_by"] = p.From // a subagent speaking under its parent's membership
		}
		out = append(out, r)
	}
	return out
}

// carryPosts moves the source lane's remarks into the destination, because the
// source is deleted immediately afterwards and anything left behind is gone.
//
// This codebase has dropped a sibling collection on a destructive op twice,
// merge and evict silently discarded queues, then announcements, and each time
// the surviving lane looked correct while the history it should have absorbed
// had simply ceased to exist. A merge is supposed to combine two lanes, so the
// members who arrive should still be able to read what they were discussing.
func (s *State) carryPosts(src, dst *Channel) {
	if len(src.Posts) == 0 {
		return
	}
	merged := append(append([]Post(nil), dst.Posts...), src.Posts...)
	// Serial order, so the combined history reads as one conversation rather
	// than one lane's posts followed by the other's.
	slices.SortStableFunc(merged, func(a, b Post) int {
		return cmp.Compare(a.Serial, b.Serial)
	})
	if excess := len(merged) - s.Limits.PostRetention; excess > 0 {
		merged = merged[excess:]
	}
	dst.Posts = merged
}
