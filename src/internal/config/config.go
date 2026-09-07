// Package config incrementally edits /etc/clash.yaml in yaml.v3 Node mode:
// it touches only proxy-providers[NAME] and the groups' use lists, preserving
// comments/anchors/other providers — never a wholesale overwrite. This is where the
// "sub import only does node management and wiring" semantics land.
package config

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/deadship2003/panoxy/internal/asset"
	"github.com/deadship2003/panoxy/internal/constants"

	"gopkg.in/yaml.v3"
)

// PlaceholderURL is the default template's placeholder subscription url value
// (SUB_URL_PLACEHOLDER). It retires automatically on the first real import (sub import)
// or on merge-conf (see MergePersonal and the placeholder cleanup in subcmds).
const PlaceholderURL = "SUB_URL_PLACEHOLDER"

// Editor holds the parsed config tree; all operations mutate memory only, Save persists.
type Editor struct {
	root *yaml.Node // DocumentNode
	path string
}

// Load parses the config file into a Node tree (comments are preserved with the nodes).
func Load(path string) (*Editor, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config: %w", err)
	}
	var root yaml.Node
	if err := yaml.Unmarshal(b, &root); err != nil {
		return nil, fmt.Errorf("failed to parse YAML: %w", err)
	}
	if root.Kind != yaml.DocumentNode || len(root.Content) == 0 {
		return nil, fmt.Errorf("config is not a valid YAML document")
	}
	return &Editor{root: &root, path: path}, nil
}

// Render encodes to a string (indent 2, matching the hand-written style) without
// persisting. Normalization: yaml.v3 emits merge keys explicitly as "!!merge <<"; here we
// restore the hand-written bare "<<" (the next parse still resolves it as a merge; keeps
// one edit from producing whole-file diff noise).
func (e *Editor) Render() (string, error) {
	normalizeMergeKeys(e.root)
	var buf []byte
	enc := yaml.NewEncoder(&nopWriter{&buf})
	enc.SetIndent(2)
	if err := enc.Encode(e.root); err != nil {
		return "", fmt.Errorf("failed to encode YAML: %w", err)
	}
	enc.Close()
	// Un-escape non-ASCII (yaml.v3 turns emoji etc. into "\U0001F503" by default —
	// functionally correct but unreadable; only codepoints >= 0x80 in \U/\u sequences
	// are processed, literal-backslash cases are unaffected).
	return unescapeNonASCII(string(buf)) + "\n", nil
}

// Save encodes and persists.
func (e *Editor) Save() error {
	out, err := e.Render()
	if err != nil {
		return err
	}
	return os.WriteFile(e.path, []byte(out), 0o644)
}

func unescapeNonASCII(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		c := s[i]
		if c == '\\' && i+1 < len(s) && (s[i+1] == 'U' || s[i+1] == 'u') {
			width := 8
			if s[i+1] == 'u' {
				width = 4
			}
			if i+2+width <= len(s) {
				if cp, ok := parseHex(s[i+2 : i+2+width]); ok && cp >= 0x80 {
					b.WriteRune(rune(cp))
					i += 2 + width
					continue
				}
			}
		}
		b.WriteByte(c)
		i++
	}
	return b.String()
}

func parseHex(s string) (int64, bool) {
	var v int64
	for i := 0; i < len(s); i++ {
		c := s[i]
		var d int64
		switch {
		case c >= '0' && c <= '9':
			d = int64(c - '0')
		case c >= 'a' && c <= 'f':
			d = int64(c-'a') + 10
		case c >= 'A' && c <= 'F':
			d = int64(c-'A') + 10
		default:
			return 0, false
		}
		v = v*16 + d
	}
	return v, true
}

func normalizeMergeKeys(n *yaml.Node) {
	if n == nil {
		return
	}
	if n.Kind == yaml.ScalarNode && n.Tag == "!!merge" {
		n.Tag = "!!str"
	}
	for _, c := range n.Content {
		normalizeMergeKeys(c)
	}
}

type nopWriter struct{ b *[]byte }

func (w *nopWriter) Write(p []byte) (int, error) { *w.b = append(*w.b, p...); return len(p), nil }

// topMap returns the top-level MappingNode.
func (e *Editor) topMap() *yaml.Node {
	return e.root.Content[0]
}

// mapGet fetches the value node for a key in a mapping; nil when absent.
func mapGet(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// mapSet replaces or appends a key/value pair (appended at the end, preserving the
// original order and comments).
func mapSet(m *yaml.Node, key string, val *yaml.Node) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content[i+1] = val
			return
		}
	}
	m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, val)
}

// mapDel deletes a key; returns false when absent.
func mapDel(m *yaml.Node, key string) bool {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content = append(m.Content[:i], m.Content[i+2:]...)
			return true
		}
	}
	return false
}

// seqAppend appends a scalar, deduplicated.
func seqAppend(s *yaml.Node, val string) {
	for _, c := range s.Content {
		if c.Value == val {
			return
		}
	}
	s.Content = append(s.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: val})
}

// seqRemove removes a scalar; returns whether anything changed.
func seqRemove(s *yaml.Node, val string) bool {
	for i, c := range s.Content {
		if c.Value == val {
			s.Content = append(s.Content[:i], s.Content[i+1:]...)
			return true
		}
	}
	return false
}

// Providers returns all provider names (in config order).
func (e *Editor) Providers() []string {
	var out []string
	if pm := mapGet(e.topMap(), "proxy-providers"); pm != nil && pm.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(pm.Content); i += 2 {
			out = append(out, pm.Content[i].Value)
		}
	}
	return out
}

// HasAnchorP checks whether the config contains the &p anchor (a precondition of sub import).
func (e *Editor) HasAnchorP() bool {
	v := mapGet(e.topMap(), "p")
	return v != nil && v.Anchor == "p"
}

// ProviderURL returns a provider's url (used by status display and placeholder detection).
func (e *Editor) ProviderURL(name string) (string, bool) {
	pm := mapGet(e.topMap(), "proxy-providers")
	if pm == nil {
		return "", false
	}
	p := mapGet(pm, name)
	if p == nil {
		return "", false
	}
	if u := mapGet(p, "url"); u != nil {
		return u.Value, true
	}
	return "", true
}

// SetProvider writes/updates a provider: url + path, the entry reusing the <<: *p
// anchor; an existing entry has only its url/path keys changed, every other key and
// comment stays as-is.
func (e *Editor) SetProvider(name, url, cacheRelPath string) error {
	if !e.HasAnchorP() {
		return fmt.Errorf("config is missing anchor &p (sub import depends on it to generate provider entries; the base template provides it)")
	}
	tm := e.topMap()
	pm := mapGet(tm, "proxy-providers")
	if pm == nil || pm.Kind != yaml.MappingNode {
		pm = &yaml.Node{Kind: yaml.MappingNode}
		mapSet(tm, "proxy-providers", pm)
	}
	entry := mapGet(pm, name)
	if entry == nil {
		entry = &yaml.Node{
			Kind: yaml.MappingNode,
			Content: []*yaml.Node{
				{Kind: yaml.ScalarNode, Tag: "!!merge", Value: "<<"},
				{Kind: yaml.AliasNode, Value: "p"},
			},
		}
		pm.Content = append(pm.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: name}, entry)
	}
	mapSet(entry, "url", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: url})
	mapSet(entry, "path", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: cacheRelPath})
	return nil
}

// SetProviderType switches a provider's read mode (file=true sets type: file to read the
// local cache without refreshing the remote; false removes the explicit type and falls
// back to the anchor <<: *p's type: http auto-refresh).
//
// Used for subscription formats mihomo cannot parse natively (sing-box/Surge/base64-
// Clash): sub import has already normalized the content into Clash YAML in the cache, so
// the provider must switch to file — otherwise the kernel re-fetches the original URL on
// restart and fails to parse again.
func (e *Editor) SetProviderType(name string, file bool) error {
	pm := mapGet(e.topMap(), "proxy-providers")
	if pm == nil || pm.Kind != yaml.MappingNode {
		return fmt.Errorf("config is missing the proxy-providers section")
	}
	entry := mapGet(pm, name)
	if entry == nil {
		return fmt.Errorf("provider %s does not exist", name)
	}
	if file {
		mapSet(entry, "type", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "file"})
	} else {
		mapDel(entry, "type")
	}
	return nil
}

// RemoveProvider deletes a provider entry.
func (e *Editor) RemoveProvider(name string) bool {
	pm := mapGet(e.topMap(), "proxy-providers")
	if pm == nil {
		return false
	}
	return mapDel(pm, name)
}

// WireProvider adds name into the use lists (add=true) or removes it (add=false).
//
// Wiring rules (the base template's optimized path first):
//  1. the top-level anchor holders pr/prd/use's use sequences — one change, every
//     consuming group follows
//  2. without anchor holders (custom config): walk proxy-groups and append to every
//     group whose use is non-empty
//
// With a non-empty groups (--group): only the named groups are touched; a group without
// a use key gets one created explicitly (overriding merge semantics).
func (e *Editor) WireProvider(name string, add bool, groups []string) int {
	tm := e.topMap()
	changed := 0
	if len(groups) == 0 {
		// Path 1: anchor holders
		for _, holder := range []string{"pr", "prd", "use"} {
			hm := mapGet(tm, holder)
			if hm == nil {
				continue
			}
			if use := mapGet(hm, "use"); use != nil && use.Kind == yaml.SequenceNode {
				if add {
					seqAppend(use, name)
				} else {
					seqRemove(use, name)
				}
				changed++
			}
		}
		if changed > 0 {
			return changed
		}
		// Path 2: custom config, walk the groups
		if gl := mapGet(tm, "proxy-groups"); gl != nil && gl.Kind == yaml.SequenceNode {
			for _, g := range gl.Content {
				if use := mapGet(g, "use"); use != nil && use.Kind == yaml.SequenceNode && len(use.Content) > 0 {
					if add {
						seqAppend(use, name)
					} else {
						seqRemove(use, name)
					}
					changed++
				}
			}
		}
		return changed
	}
	// --group explicit selection
	gl := mapGet(tm, "proxy-groups")
	if gl == nil || gl.Kind != yaml.SequenceNode {
		return 0
	}
	for _, g := range gl.Content {
		gname := mapGet(g, "name")
		if gname == nil {
			continue
		}
		for _, want := range groups {
			if gname.Value != want {
				continue
			}
			use := mapGet(g, "use")
			if use == nil || use.Kind != yaml.SequenceNode {
				use = &yaml.Node{Kind: yaml.SequenceNode}
				mapSet(g, "use", use)
			}
			if add {
				seqAppend(use, name)
			} else {
				seqRemove(use, name)
			}
			changed++
		}
	}
	return changed
}

// PruneDerived removes derived groups (region/type groups with a filter) that match no
// actual node, keeping only effective groups. The removed group names are also stripped
// from the anchor holders' (pr/prd) and the DNS group's proxies lists, avoiding dangling
// references. Returns the number of pruned groups. nodeNames must cover every provider
// (including the newly imported subscription) so groups still hit by another
// subscription are not removed by mistake.
func (e *Editor) PruneDerived(nodeNames []string) int {
	tm := e.topMap()
	gl := mapGet(tm, "proxy-groups")
	if gl == nil || gl.Kind != yaml.SequenceNode {
		return 0
	}
	var prune []string
	keep := make([]*yaml.Node, 0, len(gl.Content))
	for _, g := range gl.Content {
		f := mapGet(g, "filter")
		if f == nil || f.Value == "" {
			keep = append(keep, g) // groups without a filter (app groups / the fallback group) never get pruned
			continue
		}
		re, err := regexp.Compile(f.Value)
		if err != nil {
			keep = append(keep, g) // invalid filter kept; the embedded kernel's validation will report it
			continue
		}
		hit := false
		for _, n := range nodeNames {
			if re.MatchString(n) {
				hit = true
				break
			}
		}
		if name := mapGet(g, "name"); name != nil && !hit {
			prune = append(prune, name.Value)
		} else {
			keep = append(keep, g)
		}
	}
	if len(prune) == 0 {
		return 0
	}
	gl.Content = keep
	// Remove the pruned group names from the anchor holders' and the DNS group's proxies
	for _, holder := range []string{"pr", "prd"} {
		if hm := mapGet(tm, holder); hm != nil {
			if px := mapGet(hm, "proxies"); px != nil && px.Kind == yaml.SequenceNode {
				for _, p := range prune {
					seqRemove(px, p)
				}
			}
		}
	}
	for _, g := range gl.Content {
		if name := mapGet(g, "name"); name != nil && name.Value == "DNS" {
			if px := mapGet(g, "proxies"); px != nil && px.Kind == yaml.SequenceNode {
				for _, p := range prune {
					seqRemove(px, p)
				}
			}
		}
	}
	return len(prune)
}

// Backup / Restore / ClearBackup are the transaction companions: back up before
// mutating, restore on failure. The suffix derives from the program name
// (constants.BackupSuffix); bak and premerge share the backupFile/restoreFile impl.
func Backup(path string) error  { return backupFile(path, constants.BackupSuffix()) }
func Restore(path string) error { return restoreFile(path, constants.BackupSuffix()) }
func ClearBackup(path string)   { os.Remove(path + constants.BackupSuffix()) }

// backupFile / restoreFile are the shared implementation for bak and premerge (only the
// suffix differs).
func backupFile(path, suffix string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return os.WriteFile(path+suffix, b, 0o644)
}

func restoreFile(path, suffix string) error {
	b, err := os.ReadFile(path + suffix)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

// SetMode switches the tun/tproxy config variant (used by the mode command; the tun block
// stays consistent with the template constants).
func (e *Editor) SetMode(tproxy bool, tproxyPort int) {
	tm := e.topMap()
	if tproxy {
		mapDel(tm, "tun")
		mapSet(tm, "tproxy-port", &yaml.Node{
			Kind: yaml.ScalarNode, Tag: "!!int", Value: fmt.Sprint(tproxyPort),
			LineComment: "TPROXY mode (mark / policy routing managed by the " + constants.ProgName + " firewall)",
		})
		return
	}
	mapDel(tm, "tproxy-port")
	tun := &yaml.Node{Kind: yaml.MappingNode}
	for _, kv := range asset.TunParams {
		tag := "!!str"
		if kv[1] == "true" || kv[1] == "1500" {
			tag = "!!bool"
			if kv[1] == "1500" {
				tag = "!!int"
			}
		}
		tun.Content = append(tun.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: kv[0]},
			&yaml.Node{Kind: yaml.ScalarNode, Tag: tag, Value: kv[1]})
	}
	exc := &yaml.Node{Kind: yaml.SequenceNode}
	for _, cidr := range asset.TunRouteExclude {
		exc.Content = append(exc.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: cidr})
	}
	tun.Content = append(tun.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "route-exclude-address",
			LineComment: "exclude loopback / LAN to prevent proxy loops"}, exc)
	mapSet(tm, "tun", tun)
}

// SetPath changes the Save target (for previews / temporary validation).
func (e *Editor) SetPath(p string) { e.path = p }

// Path returns the current persist target.
func (e *Editor) Path() string { return e.path }
