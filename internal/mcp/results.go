package mcp

import (
	"context"
	"sort"

	"github.com/agenxy/dibs/internal/core"
)

// laneRead keeps the engine's membership-gated announcement read intact and
// enriches its presentation with the identities the board already exposes.
//
// Returning only members:4 told an agent that coordination was needed while
// withholding who to contact: reported by a real agent reader trying to recover
// context. The count stays for compatibility; member_names supplies the missing
// addresses. This deliberately does NOT acknowledge an announcement or change
// membership merely because somebody read the agent.
func (s *Server) laneRead(ctx context.Context, token, agent string, limit int) (core.Result, error) {
	res, err := s.eng.LaneRead(ctx, token, agent, limit)
	if err != nil || res["lane_id"] == nil {
		return res, err
	}
	board, err := s.eng.Board(ctx)
	if err != nil {
		return nil, err
	}
	names := memberNames(board, agent)
	res["member_names"] = names
	return res, nil
}

func memberNames(board core.Result, agent string) []string {
	names := []string{}
	for _, space := range asMaps(board["spaces"]) {
		if space["id"] != agent {
			continue
		}
		for _, member := range asMaps(space["members"]) {
			if name, ok := member["agent"].(string); ok && name != "" {
				names = append(names, name)
			}
		}
		break
	}
	sort.Strings(names)
	return names
}
