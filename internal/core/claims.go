package core

import (
	"path/filepath"
	"strings"
)

// cleanPath normalizes a claim path: absolute, cleaned, no trailing slash.
func cleanPath(p string) string {
	p = filepath.Clean(p)
	if len(p) > 1 {
		p = strings.TrimSuffix(p, "/")
	}
	return p
}

// pathsOverlap reports whether one path is equal to or an ancestor of the other.
func pathsOverlap(a, b string) bool {
	if a == b {
		return true
	}
	return strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/")
}

// overlapping returns all live claims overlapping path, excluding a lane's own.
func (s *State) overlapping(path, excludeLane string) []*Claim {
	var out []*Claim
	for _, c := range s.Claims {
		if c.Lane != excludeLane && pathsOverlap(c.Path, path) {
			out = append(out, c)
		}
	}
	return out
}

// Signal strengths for an overlap. The distinction is the whole point: two
// agents editing the same file is NORMAL (version control solved that, and
// suppressing it would destroy fleet parallelism). Two agents pursuing the same
// OBJECTIVE is the actual waste — a measured collision burned ~3,900 diff lines
// across three PRs chasing one goal, in files that only incidentally overlapped.
const (
	SignalSameObjective = "same-objective" // strong: probable duplicated effort
	SignalSamePaths     = "same-paths"     // weak: concurrent work, usually fine
	SignalClaim         = "claim"          // someone asked to be left alone here
)

// SlotOverlap is another lane's activity that relates to what you just declared.
// Informational in every case — declaring work never fails.
type SlotOverlap struct {
	Lane   string   `json:"lane"`
	Signal string   `json:"signal"`
	Kind   string   `json:"kind"`           // "slot" | "claim"
	Text   string   `json:"text,omitempty"` // their slot text / claim note
	Refs   []string `json:"refs,omitempty"` // the shared objective ids, if any
	Path   string   `json:"path,omitempty"` // the overlapping path, if any
	Mode   string   `json:"mode,omitempty"` // claims only
}

// Strong reports whether this overlap likely means duplicated effort.
func (o SlotOverlap) Strong() bool { return o.Signal == SignalSameObjective }

// overlapsFor finds other lanes related to a declaration. Objectives (shared
// refs like "pr:1186", "gate:typos", "issue:1140") are the primary key; paths
// are a weak secondary hint; claims are surfaced because someone asked.
func (s *State) overlapsFor(refs, dirs []string, excludeLane string) []SlotOverlap {
	var out []SlotOverlap
	seen := map[string]bool{}
	add := func(o SlotOverlap) {
		k := o.Lane + "\x00" + o.Signal + "\x00" + o.Path + "\x00" + strings.Join(o.Refs, ",")
		if !seen[k] {
			seen[k] = true
			out = append(out, o)
		}
	}
	want := map[string]bool{}
	for _, r := range refs {
		if r = normRef(r); r != "" {
			want[r] = true
		}
	}
	for _, l := range s.Lanes {
		if l.ID == excludeLane || l.Status == StatusClosed || l.Status == StatusArchived {
			continue
		}
		for _, sl := range l.Slots {
			if shared := sharedRefs(want, sl.Refs); len(shared) > 0 {
				add(SlotOverlap{
					Lane: l.ID, Signal: SignalSameObjective, Kind: "slot",
					Text: sl.Text, Refs: shared,
				})
				continue // strong signal already reported for this slot
			}
			if p := firstOverlappingPath(dirs, sl.Dirs); p != "" {
				add(SlotOverlap{
					Lane: l.ID, Signal: SignalSamePaths, Kind: "slot",
					Text: sl.Text, Refs: sl.Refs, Path: p,
				})
			}
		}
	}
	for _, d := range dirs {
		for _, c := range s.overlapping(cleanPath(d), excludeLane) {
			add(SlotOverlap{
				Lane: c.Lane, Signal: SignalClaim, Kind: "claim",
				Text: c.Note, Path: c.Path, Mode: c.Mode,
			})
		}
	}
	sortOverlaps(out)
	return out
}

// normRef canonicalizes an objective id so "PR #101" and "pr:1186" match.
func normRef(r string) string {
	r = strings.ToLower(strings.TrimSpace(r))
	r = strings.NewReplacer(" ", "", "#", "", "/", ":").Replace(r)
	return strings.Trim(r, ":")
}

func sharedRefs(want map[string]bool, theirs []string) []string {
	var out []string
	for _, r := range theirs {
		if want[normRef(r)] {
			out = append(out, r)
		}
	}
	return out
}

func firstOverlappingPath(mine, theirs []string) string {
	for _, a := range mine {
		for _, b := range theirs {
			if pathsOverlap(cleanPath(a), cleanPath(b)) {
				return b
			}
		}
	}
	return ""
}

func sortOverlaps(o []SlotOverlap) {
	for i := 1; i < len(o); i++ {
		for j := i; j > 0 && overlapLess(o[j], o[j-1]); j-- {
			o[j-1], o[j] = o[j], o[j-1]
		}
	}
}

// overlapLess sorts strong signals first — the agent should read those.
func overlapLess(a, b SlotOverlap) bool {
	if a.Strong() != b.Strong() {
		return a.Strong()
	}
	if a.Lane != b.Lane {
		return a.Lane < b.Lane
	}
	return a.Path < b.Path
}

// releaseClaims drops all claims held by lane, returning the released paths.
func (s *State) releaseClaims(lane string) []string {
	var kept []*Claim
	var released []string
	for _, c := range s.Claims {
		if c.Lane == lane {
			released = append(released, c.Path)
		} else {
			kept = append(kept, c)
		}
	}
	s.Claims = kept
	return released
}
