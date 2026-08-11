package mcp

import (
	"context"
	"sort"

	"github.com/agenxy/lanes/internal/core"
)

// laneRead keeps the engine's membership-gated announcement read intact and
// enriches its presentation with the identities the board already exposes.
//
// Returning only members:4 told an agent that coordination was needed while
// withholding who to contact: reported by a real lane reader trying to recover
// context. The count stays for compatibility; member_names supplies the missing
// addresses. This deliberately does NOT acknowledge an announcement or change
// membership merely because somebody read the lane.
func (s *Server) laneRead(ctx context.Context, token, lane string, limit int) (core.Result, error) {
	res, err := s.eng.LaneRead(ctx, token, lane, limit)
	if err != nil || res["lane_id"] == nil {
		return res, err
	}
	board, err := s.eng.Board(ctx)
	if err != nil {
		return nil, err
	}
	names := memberNames(board, lane)
	res["member_names"] = names
	return res, nil
}

func memberNames(board core.Result, lane string) []string {
	names := []string{}
	for _, channel := range asMaps(board["channels"]) {
		if channel["id"] != lane {
			continue
		}
		for _, member := range asMaps(channel["members"]) {
			if name, ok := member["agent"].(string); ok && name != "" {
				names = append(names, name)
			}
		}
		break
	}
	sort.Strings(names)
	return names
}
