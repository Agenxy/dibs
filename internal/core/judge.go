package core

// evidenceAgainstMembers compares one live declaration against each member's own
// live declaration and keeps the strongest relation.
//
// Slot-to-slot, deliberately. Comparing against ch.Predicted: the union of
// everything every member has ever been predicted to touch: made an agent an
// easier target the longer it lived, because that union grows on every join and
// never shrinks. Measured: the same unrelated newcomer scored 0.0000 against a
// one-member agent and 0.1000 against the same agent with five.
//
// The union is still what generated this candidate, which is the job breadth is
// good for. It is not what decides.
// The third return says whether there was anything to judge AGAINST: at least
// one member holding a live declaration with a footprint. It is not a detail.
// "I compared you against this space's members and none resembles you" and "this
// space's members have declared nothing I could compare you to" are opposite
// facts, and scoring both as zero made every agent whose members had not yet
// called declare permanently invisible: including the ordinary case of an
// agent opening an agent for work it is about to start.
func (s *State) evidenceAgainstMembers(
	ch *Space, mine Slot, myCWD, repo string, discount map[string]float64, lens RepoLens,
) (Evidence, Relation, bool) {
	best, bestRel := Evidence{SameRepo: true}, RelationNone
	compared := false
	for agent := range ch.Members {
		l := s.Agents[agent]
		if l == nil {
			continue
		}
		theirCWD := ""
		if l.Agent != nil {
			theirCWD = l.Agent.CWD
		}
		// The two ROWS decide the repository question before any path does:
		// see identityFirst.
		if pair, ok := lens.(identityFirst); ok {
			pair.them = l
			lens = pair
		}
		for _, theirs := range l.Slots {
			// Their claims get the same scrutiny as the newcomer's; a key holds
			// or it does not, whichever side wrote it down.
			theirs.Refs = s.validatedRefs(agent, theirs.Refs)
			// A slot the scorer had no opinion about cannot testify to
			// dissimilarity either: it was never measured.
			compared = compared || len(theirs.Predicted) > 0
			ev := EvidenceBetween(mine, theirs, myCWD, theirCWD, repo, discount, lens)
			rel := ev.Classify()
			// Strongest relation wins; among equals, the closest declaration.
			// Ranking on relation alone left the reported evidence arbitrary among
			// several members sharing a relation, so an agent could be shown the
			// least similar of the peers it actually matched.
			if relationRank(rel) > relationRank(bestRel) ||
				(relationRank(rel) == relationRank(bestRel) && ev.Semantic > best.Semantic) {
				best, bestRel = ev, rel
			}
		}
	}
	return best, bestRel, compared
}

// judgedScore replaces a union-derived score with the closest live declaration's.
//
// The union FOUND this candidate; it does not get to judge it. ch.Predicted is
// every member's footprint merged, and merging is monotonic: it grows on each
// join and never shrinks, so an agent became an easier target the longer it lived,
// and an agent that matched more gained members and gained surface by gaining them.
// Measured before this changed: the same unrelated newcomer scored 0.0000 against
// a one-member agent and 0.1000 against the same agent with five, crossing a real
// fleet's join bar with no change to its work and none to the agent's topic.
//
// Breadth is the right property for FINDING candidates and the wrong one for
// judging them. The score now comes from the closest single live declaration,
// which is the thing an agent can actually be duplicating.
// There is deliberately no fallback to unionScore. Keeping one meant that an agent
// where NO member matched still scored on the merged footprint, which is the
// accretion bug itself, surviving in the one branch that looked harmless. If no
// live declaration in the agent resembles this one, the honest score is zero and
// `worthless` drops the candidate.
func judgedScore(union float64, ev Evidence, compared bool) float64 {
	if !compared {
		// Nothing was measured, so there is no verdict to prefer over the union.
		// Scoring this zero does not express doubt: it deletes the agent from
		// every future match, silently and permanently, and an agent opened for work
		// that has not been declared yet is the commonest shape there is: the
		// space e2e opens one on exactly that path and it vanished.
		//
		// The union may still overstate an agent that many agents joined. That is a
		// worse estimate; invisibility is not an estimate at all.
		return union
	}
	return ev.Semantic
}
