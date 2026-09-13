package boardconfig

import "testing"

// `name` is a hostname and nothing else: Remap routes it to this board and
// the daemon accepts it as an origin, so a scheme, a port or a path in it
// would be routed nowhere and trusted anyway.
func TestABoardNameIsAHostname(t *testing.T) {
	for _, good := range []string{"", "dibs", "board.lab", "hub-1.home.arpa", "DIBS"} {
		if err := (Config{Name: good}).ValidateName(); err != nil {
			t.Errorf("name %q refused: %v", good, err)
		}
	}
	for _, bad := range []string{"http://dibs", "dibs:4777", "dibs/", " dibs", "dibs ", ".dibs", "dibs.", "-dibs", "a..b", "dibs_board"} {
		if err := (Config{Name: bad}).ValidateName(); err == nil {
			t.Errorf("name %q accepted", bad)
		}
	}
}
