package main

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// standardLaunchPolicy changes only the top-level Background string. Parsing
// XML, rather than searching for a spelling, keeps nested operator settings
// and comments out of the decision. The original bytes remain the authority
// for every field that this migration does not own.
func standardLaunchPolicy(body []byte) ([]byte, error) {
	if len(body) > 1<<20 {
		return nil, fmt.Errorf("LaunchAgent exceeds 1 MiB")
	}
	root, err := readPolicyPlist(body)
	if err != nil {
		return nil, err
	}
	policy, err := topLevelPolicy(root)
	if err != nil {
		return nil, err
	}
	if policy == nil || policy.text != "Background" {
		return nil, nil // An explicit operator policy is left alone.
	}
	out := append([]byte(nil), body[:policy.start]...)
	out = append(out, "Standard"...)
	out = append(out, body[policy.end:]...)
	return out, nil
}

func readPolicyPlist(body []byte) (*policyElement, error) {
	d := xml.NewDecoder(bytes.NewReader(body))
	var root *policyElement
	for {
		tok, err := d.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("reading XML LaunchAgent: %w", err)
		}
		switch x := tok.(type) {
		case xml.StartElement:
			if root != nil {
				return nil, fmt.Errorf("multiple plist roots")
			}
			root, err = readPolicyElement(d, x, 0)
			if err != nil {
				return nil, err
			}
		case xml.CharData:
			if strings.TrimSpace(string(x)) != "" {
				return nil, fmt.Errorf("text outside plist")
			}
		}
	}
	return root, nil
}

func topLevelPolicy(root *policyElement) (*policyElement, error) {
	if root == nil || root.name != "plist" || len(root.children) != 1 || root.children[0].name != "dict" {
		return nil, fmt.Errorf("expected an XML plist with one top-level dictionary")
	}
	dict := root.children[0]
	if strings.TrimSpace(dict.text) != "" || len(dict.children)%2 != 0 {
		return nil, fmt.Errorf("invalid top-level plist dictionary")
	}
	seen := map[string]bool{}
	var policy *policyElement
	for i := 0; i < len(dict.children); i += 2 {
		key, val := dict.children[i], dict.children[i+1]
		if key.name != "key" || len(key.children) != 0 || seen[key.text] {
			return nil, fmt.Errorf("invalid or duplicate top-level plist key %q", key.text)
		}
		seen[key.text] = true
		if key.text == "ProcessType" {
			if val.name != "string" || len(val.children) != 0 {
				return nil, fmt.Errorf("ProcessType must be a string")
			}
			policy = val
		}
	}
	return policy, nil
}

type policyElement struct {
	name, text string
	start, end int64
	children   []*policyElement
}

func readPolicyElement(d *xml.Decoder, start xml.StartElement, depth int) (*policyElement, error) {
	if depth > 64 || start.Name.Space != "" {
		return nil, fmt.Errorf("unsupported plist nesting or namespace")
	}
	n := &policyElement{name: start.Name.Local, start: d.InputOffset()}
	for {
		offset := d.InputOffset()
		tok, err := d.Token()
		if err != nil {
			return nil, err
		}
		switch x := tok.(type) {
		case xml.StartElement:
			child, err := readPolicyElement(d, x, depth+1)
			if err != nil {
				return nil, err
			}
			n.children = append(n.children, child)
		case xml.CharData:
			n.text += string(x)
		case xml.EndElement:
			n.end = offset
			return n, nil
		}
	}
}

func (p *plan) policyFailure(err error) error {
	return fmt.Errorf("cannot update scheduling in %s, so nothing has been stopped: %w; "+
		"inspect the unit, convert a binary plist with `plutil -convert xml1`, or set its "+
		"top-level ProcessType to Standard, then run `dibs upgrade` again", p.unit, err)
}

func (p *plan) checkPolicyUnit() error {
	st, err := os.Lstat(p.unit) // #nosec G703 -- board-matching user service unit.
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() {
		return fmt.Errorf("policy migration requires a regular unit file")
	}
	current, err := os.ReadFile(p.unit) // #nosec G304,G703 -- same unit.
	if err != nil {
		return err
	}
	if !bytes.Equal(current, p.policyBefore) {
		return fmt.Errorf("unit changed after upgrade planning; run upgrade again")
	}
	return nil
}

func (p *plan) policyWritable() error {
	if err := unitIsWritable(p.unit); err != nil {
		return err
	}
	backup := p.unit + ".replaced"
	if st, err := os.Lstat(backup); err == nil {
		if !st.Mode().IsRegular() {
			return fmt.Errorf("backup %s is not a regular file", backup)
		}
		if err := unitIsWritable(backup); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(p.unit), ".dibs-policy-check-*") // #nosec G703
	if err != nil {
		return err
	}
	name := f.Name()
	err = f.Close()
	if rerr := os.Remove(name); err == nil {
		err = rerr
	}
	return err
}

func (p *plan) migratePolicy() error {
	if err := p.checkPolicyUnit(); err != nil {
		return fmt.Errorf("scheduling migration refused: %w", err)
	}
	if err := p.policyWritable(); err != nil {
		return err
	}
	// Unlike whole-unit regeneration, a policy migration must retain the old
	// bytes successfully before changing anything. Failure leaves them intact.
	if err := os.WriteFile(p.unit+".replaced", p.policyBefore, 0o600); err != nil { // #nosec G306,G703
		return fmt.Errorf("retaining the original LaunchAgent: %w", err)
	}
	st, err := os.Stat(p.unit) // #nosec G703
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(p.unit), ".dibs-policy-*") // #nosec G703
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	if err := f.Chmod(st.Mode().Perm()); err != nil {
		_ = f.Close()
		return err
	}
	if _, err := f.Write(p.policyAfter); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), p.unit); err != nil { // #nosec G703
		return err
	}
	say("  changed %s ProcessType from Background to Standard; other settings retained", p.unit)
	return nil
}
