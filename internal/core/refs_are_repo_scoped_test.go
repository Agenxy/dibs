package core

import "testing"

// A ref names something inside ONE repository. "issue:42" in a payments API and
// "issue:42" in a marketing site are the same number and nothing else.
//
// Reported by an adversarial review, which registered two agents from two Git
// checkouts, had both declare `issue:42`, and got back the strongest signal
// Dibs emits: "another agent is already pursuing the same objective". That tells
// an agent to stop or to go and coordinate over work nobody else is doing, and
// on a machine running several projects it fires constantly, because issue and
// PR numbers are small integers that every project reuses.
//
// Both directions are asserted in one test on purpose. Scoping refs by
// repository can be "fixed" by simply reporting less, and a board that never
// warns is worse than one that warns wrongly: the same-repository case is the
// entire reason the signal exists.
func TestRefsOnlyCollideInsideOneRepository(t *testing.T) {
	const ref = "issue:42"

	for _, tc := range []struct {
		what      string
		a, b      *AgentInfo
		wantAlarm bool
		why       string
	}{
		{
			what:      "two projects with different remotes and different histories",
			a:         &AgentInfo{CWD: "/w/api", RepoDir: "/w/api/.git", RepoRemote: "github.com/acme/api", RepoRoots: "aaa111"},
			b:         &AgentInfo{CWD: "/w/site", RepoDir: "/w/site/.git", RepoRemote: "github.com/acme/site", RepoRoots: "zzz999"},
			wantAlarm: false,
			why:       "nothing links them: different upstreams and no commit in common",
		},
		{
			what:      "two local repositories, neither with a remote",
			a:         &AgentInfo{CWD: "/w/one", RepoDir: "/w/one/.git", RepoRoots: "aaa111"},
			b:         &AgentInfo{CWD: "/w/two", RepoDir: "/w/two/.git", RepoRoots: "zzz999"},
			wantAlarm: false,
			why:       "with no upstream to share, different common directories cannot be one project",
		},
		{
			what:      "one repository, two agents",
			a:         &AgentInfo{CWD: "/w/api", RepoDir: "/w/api/.git", RepoRemote: "github.com/acme/api"},
			b:         &AgentInfo{CWD: "/w/api/internal", RepoDir: "/w/api/.git", RepoRemote: "github.com/acme/api"},
			wantAlarm: true,
			why:       "this is duplicated effort, and catching it is why Dibs exists",
		},
		{
			what:      "two linked worktrees of one repository",
			a:         &AgentInfo{CWD: "/w/api", RepoDir: "/w/api/.git", RepoRoots: "aaa111"},
			b:         &AgentInfo{CWD: "/w/api-hotfix", RepoDir: "/w/api/.git", RepoRoots: "aaa111"},
			wantAlarm: true,
			why:       "a linked worktree shares the common directory: same repository, same issue 42",
		},
		{
			what:      "two clones of one upstream",
			a:         &AgentInfo{CWD: "/w/api", RepoDir: "/w/api/.git", RepoRemote: "github.com/acme/api"},
			b:         &AgentInfo{CWD: "/w/api2", RepoDir: "/w/api2/.git", RepoRemote: "github.com/acme/api"},
			wantAlarm: true,
			why:       "clones of one upstream share its issue numbers",
		},
		{
			what:      "an agent that said nothing about where it is",
			a:         &AgentInfo{CWD: "/w/api", RepoDir: "/w/api/.git", RepoRemote: "github.com/acme/api"},
			b:         nil,
			wantAlarm: true,
			why:       "absence of evidence is not evidence of separation; a missed collision costs more",
		},
		{
			what:      "one identified, one outside any checkout",
			a:         &AgentInfo{CWD: "/w/api", RepoDir: "/w/api/.git", RepoRemote: "github.com/acme/api"},
			b:         &AgentInfo{CWD: "/tmp/scratch"},
			wantAlarm: true,
			why:       "an unidentifiable directory could be anywhere, including inside the same project",
		},
		{
			// The case an adversarial review found, and the expensive direction:
			// a MISSED collision inside one project. Both facts that usually
			// decide this say "different", and only the shared history says
			// otherwise. It is right and the other two are blind here.
			what:      "two clones of one upstream, one with origin removed",
			a:         &AgentInfo{CWD: "/w/api", RepoDir: "/w/api/.git", RepoRemote: "github.com/acme/api", RepoRoots: "aaa111"},
			b:         &AgentInfo{CWD: "/w/api-copy", RepoDir: "/w/api-copy/.git", RepoRoots: "aaa111"},
			wantAlarm: true,
			why:       "a clone keeps its history when it loses its remote, and issue 42 is still issue 42",
		},
		{
			// Found by running the real daemon rather than by reasoning about
			// it. Treating this as unknown looked careful and produced a false
			// alarm between every locally created repository and every other
			// project on the machine, which is most machines.
			what:      "one has a remote, the other is an unrelated local repository",
			a:         &AgentInfo{CWD: "/w/api", RepoDir: "/w/api/.git", RepoRemote: "github.com/acme/api", RepoRoots: "aaa111"},
			b:         &AgentInfo{CWD: "/w/scratch", RepoDir: "/w/scratch/.git", RepoRoots: "zzz999"},
			wantAlarm: false,
			why:       "separate histories are separate projects, whatever their remotes say",
		},
		{
			// This was asserted the other way round and it was wrong. Reported by
			// a review that ran real `git subtree add` operations: importing a
			// dependency brings its whole history, so two unrelated projects that
			// vendored the same thing each carry their own root plus the shared
			// one. Any-commit-in-common fused them and fired the strongest signal
			// Dibs has between strangers, which is how a signal stops being
			// believed.
			what:      "two unrelated projects that vendored the same dependency by subtree",
			a:         &AgentInfo{CWD: "/w/one", RepoDir: "/w/one/.git", RepoRemote: "github.com/acme/one", RepoRoots: "aaa111 vendor22"},
			b:         &AgentInfo{CWD: "/w/two", RepoDir: "/w/two/.git", RepoRemote: "github.com/acme/two", RepoRoots: "ccc333 vendor22"},
			wantAlarm: false,
			why:       "a shared import is not a shared project; each still has its own root",
		},
		{
			// A `--single-branch` clone of an orphan branch shares no commit with
			// its sibling, and a history rewritten by filter-repo shares none
			// with the original. Both are one project, and both were missed while
			// history outranked the remote.
			what:      "one upstream, two clones whose histories have nothing in common",
			a:         &AgentInfo{CWD: "/w/a", RepoDir: "/w/a/.git", RepoRemote: "github.com/acme/api", RepoRoots: "aaa111"},
			b:         &AgentInfo{CWD: "/w/b", RepoDir: "/w/b/.git", RepoRemote: "github.com/acme/api", RepoRoots: "zzz999"},
			wantAlarm: true,
			why:       "the configured upstream is the same, which outranks a history that legitimately diverged",
		},
		{
			what:      "a shallow clone whose sibling renamed its remote",
			a:         &AgentInfo{CWD: "/w/a", RepoDir: "/w/a/.git", RepoRemote: "github.com/agenxy/homebrew-agents"},
			b:         &AgentInfo{CWD: "/w/b", RepoDir: "/w/b/.git", RepoRemote: "github.com/agenxy/homebrew-tap"},
			wantAlarm: true,
			why:       "no roots to compare and remotes that cannot be reconciled locally: unknown, so warn",
		},
		{
			what:      "an unborn repository with no commits yet",
			a:         &AgentInfo{CWD: "/w/api", RepoDir: "/w/api/.git", RepoRoots: "aaa111"},
			b:         &AgentInfo{CWD: "/w/fresh", RepoDir: "/w/fresh/.git"},
			wantAlarm: true,
			why:       "no history is not evidence of different history, and a miss costs more than a warning",
		},
		{
			// Changed deliberately. Remote used to outrank history, and that lost
			// real collisions: GitHub paths are case-insensitive and a renamed
			// repository answers to both names, so one repository compared as
			// two. Objects cannot be renamed, so history goes first, and forks
			// come along with it. Usually the right answer anyway, since a
			// fork's tracker is normally disabled and its refs name the upstream
			// one, so both agents mean the same issue 42.
			what:      "two forks of one upstream, which share history",
			a:         &AgentInfo{CWD: "/w/api", RepoDir: "/w/api/.git", RepoRemote: "github.com/acme/api", RepoRoots: "aaa111"},
			b:         &AgentInfo{CWD: "/w/fork", RepoDir: "/w/fork/.git", RepoRemote: "github.com/kim/api", RepoRoots: "aaa111"},
			wantAlarm: true,
			why:       "shared history is the stronger fact, and a fork's refs usually name the upstream tracker",
		},
		{
			what:      "one repository whose clones spell the remote with different case",
			a:         &AgentInfo{CWD: "/w/a", RepoDir: "/w/a/.git", RepoRemote: "github.com/Agenxy/Dibs", RepoRoots: "aaa111"},
			b:         &AgentInfo{CWD: "/w/b", RepoDir: "/w/b/.git", RepoRemote: "github.com/agenxy/dibs", RepoRoots: "aaa111"},
			wantAlarm: true,
			why:       "GitHub paths are case-insensitive, so these are two names for one repository",
		},
		{
			what:      "one repository seen through a rename redirect",
			a:         &AgentInfo{CWD: "/w/a", RepoDir: "/w/a/.git", RepoRemote: "github.com/agenxy/homebrew-agents", RepoRoots: "aaa111"},
			b:         &AgentInfo{CWD: "/w/b", RepoDir: "/w/b/.git", RepoRemote: "github.com/agenxy/homebrew-tap", RepoRoots: "aaa111"},
			wantAlarm: true,
			why:       "a renamed repository still serves its old path, so a stale clone url is not a different project",
		},
	} {
		t.Run(tc.what, func(t *testing.T) {
			s := NewState("test", DefaultLimits())
			s.Agents = map[string]*Agent{
				"mine":   {ID: "mine", Name: "mine", Agent: tc.a},
				"theirs": {ID: "theirs", Name: "theirs", Agent: tc.b},
			}
			s.Agents["theirs"].Slots = map[string]Slot{"now": {Text: "fixing the thing", Refs: []string{ref}}}

			got := s.overlapsFor([]string{ref}, nil, "mine")
			var strong bool
			for _, o := range got {
				if o.Strong() {
					strong = true
				}
			}
			if strong != tc.wantAlarm {
				verb := "did not report"
				if strong {
					verb = "reported"
				}
				t.Errorf("%s %s a shared objective; want alarm=%v.\n  %s",
					tc.what, verb, tc.wantAlarm, tc.why)
			}
		})
	}
}

// The gate must not silence the weak signal too. Paths are absolute, so they
// cannot collide across projects in the first place, and quietly widening a
// repository gate into "report nothing about strangers" would remove a real
// warning that costs nothing to keep.
func TestScopingRefsDoesNotSilencePathOverlap(t *testing.T) {
	s := NewState("test", DefaultLimits())
	shared := "/shared/vendor/lib"
	s.Agents = map[string]*Agent{
		"mine": {ID: "mine", Name: "mine", Agent: &AgentInfo{
			CWD: "/w/api", RepoDir: "/w/api/.git", RepoRemote: "github.com/acme/api",
		}},
		"theirs": {ID: "theirs", Name: "theirs", Agent: &AgentInfo{
			CWD: "/w/site", RepoDir: "/w/site/.git", RepoRemote: "github.com/acme/site",
		}},
	}
	s.Agents["theirs"].Slots = map[string]Slot{"now": {Text: "vendoring", Dirs: []string{shared}}}

	got := s.overlapsFor(nil, []string{shared}, "mine")
	if len(got) == 0 {
		t.Fatal("two agents editing one absolute path were not told about each other: " +
			"a shared directory is a real overlap no matter whose project it belongs to")
	}
	for _, o := range got {
		if o.Signal != SignalSamePaths {
			t.Errorf("expected a same-paths signal, got %q", o.Signal)
		}
	}
}
