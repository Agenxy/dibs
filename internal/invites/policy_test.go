package invites

import (
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/boardconfig"
)

func TestInvitationIssuancePolicyDoesNotNarrowHumanOrGrantLocalStaffImplicitly(t *testing.T) {
	for _, tc := range []struct {
		who                      string
		human, coordinator, want bool
	}{
		{"any", false, false, true},
		{"coordinator", false, false, false},
		{"coordinator", false, true, true},
		{"human", false, true, false},
		{"human", true, false, true},
	} {
		s := &Service{URL: "https://board.example.com", Policy: boardconfig.InvitesConfig{Who: tc.who}}
		_, ttl, err := s.mintPolicy(Issuance{By: "issuer", Human: tc.human}, tc.coordinator, 0)
		if (err == nil) != tc.want {
			t.Fatalf("%+v: %v", tc, err)
		}
		if err == nil && ttl != 7*24*time.Hour {
			t.Fatalf("default ttl: %v", ttl)
		}
	}
	s := &Service{URL: "https://board.example.com", Policy: boardconfig.InvitesConfig{MaxTTLS: 3600}}
	_, ttl, err := s.mintPolicy(Issuance{By: "issuer"}, false, 0)
	if err != nil || ttl != time.Hour {
		t.Fatalf("lower policy default: %v %v", ttl, err)
	}
	if _, _, err := s.mintPolicy(Issuance{By: "issuer"}, false, 3601); err == nil {
		t.Fatal("agent exceeded policy ttl")
	}
	if _, _, err := s.mintPolicy(Issuance{Human: true}, false, 30*24*3600); err != nil {
		t.Fatal("human limited by agent policy:", err)
	}
}
