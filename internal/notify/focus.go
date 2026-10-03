package notify

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
)

// Presentation is evidence about filtering, never evidence that a person saw
// a notification. Shown is "unknown" while Focus is active,
// or absent otherwise. App filters are intentionally not inspected.
type Presentation struct {
	Focus  string `json:"focus,omitempty"`
	Shown  string `json:"shown,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// FocusPresentation runs outside the writer. These private, versioned files
// are observations, not an OS visibility receipt. Unknown shapes stay unknown.
func FocusPresentation() Presentation {
	if goos != "darwin" {
		return Presentation{}
	}
	id := focusOn()
	if id == "" {
		return Presentation{}
	}
	p := Presentation{Focus: id, Shown: "unknown"}
	p.Reason = focusReason(id)
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	db := filepath.Join(home, "Library", "DoNotDisturb", "DB")
	mode, ok := focusRecord(filepath.Join(db, "ModeConfigurations.json"), "modeConfigurations", id)
	if !ok {
		return p
	}
	var name struct {
		Name string `json:"name"`
	}
	if json.Unmarshal(mode["mode"], &name) == nil && len(name.Name) > 0 && len(name.Name) <= 100 {
		p.Focus = name.Name
		p.Reason = focusReason(name.Name)
	}
	return p
}

func focusReason(name string) string {
	return "Focus " + name + " is on and may hold this; not confirmed seen. " +
		"To allow banners, add Dibs to " + name + "'s Allowed Apps in System Settings > Focus, or turn Focus off."
}

func focusRecord(path, key, id string) (map[string]json.RawMessage, bool) {
	b, err := readFocusFile(path)
	if err != nil {
		return nil, false
	}
	var doc struct {
		Header struct {
			Version int `json:"version"`
		} `json:"header"`
		Data []map[string]map[string]map[string]json.RawMessage `json:"data"`
	}
	if json.Unmarshal(b, &doc) != nil || doc.Header.Version != 3 || len(doc.Data) != 1 {
		return nil, false
	}
	m := doc.Data[0][key]["com.apple.focus."+id]
	if m == nil {
		m = doc.Data[0][key][id]
	}
	return m, m != nil
}

func readFocusFile(path string) ([]byte, error) {
	// #nosec G304 -- fixed OS observation files under the current user's home.
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, 1<<20+1))
	if len(b) > 1<<20 {
		return nil, io.ErrShortBuffer
	}
	return b, err
}
