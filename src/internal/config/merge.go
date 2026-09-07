// merge-conf core: additive merge (same-name groups merge at field level, not replace).
//
// Merge strategy (user-confirmed):
//
//	same-name group:  field-level merge (proxies/use union, scalars overridden by the
//	                  personal side, personal-added fields carried in)
//	new group:        appended at the end
//	base groups:      kept (never deleted, references never dangle)
//	rules:            personal first (matched earlier) + base fallback (MATCH last, deduped)
//	backup:           before merging -> <prog>-premerge suffix; auto-restore on failure;
//	                  --rollback for a manual revert
package config

import (
	"fmt"
	"os"

	"github.com/deadship2003/panoxy/internal/constants"

	"gopkg.in/yaml.v3"
)

type MergeOpts struct {
	DNSMine     bool // the personal dns section takes over (listen still forced to [::]:1053 dual-stack)
	NoWire      bool // do not wire base subscriptions into groups
	NoProxyWire bool // do not append personal proxies into groups
}

type MergeReport struct {
	GroupsMerged  []string // merged by name
	GroupsAdded   []string // added from personal
	GroupsKept    []string // kept from base
	RulesPersonal int
	RulesBase     int
	RulesDeduped  int
	Taken         []string // taken over (personal)
	Kept          []string // kept (base)
	Providers     struct {
		BaseKept []string
		Personal []string
		Conflict []string
	}
	RuleProvidersAdded []string
	PersonalProxies    []string
	Adjustments        []string
	BackupPath         string // premerge backup path (empty = not backed up)
}

// PremergeBackup backs up before merging (for --rollback restores).
func PremergeBackup(confPath string) (string, error) {
	dst := confPath + constants.PremergeSuffix()
	if err := backupFile(confPath, constants.PremergeSuffix()); err != nil {
		return "", err
	}
	return dst, nil
}

// PremergeRestore restores from the premerge backup.
func PremergeRestore(confPath string) error {
	if err := restoreFile(confPath, constants.PremergeSuffix()); err != nil {
		return fmt.Errorf("no premerge backup: %w", err)
	}
	return nil
}

// PremergeExists reports whether a premerge backup exists.
func PremergeExists(confPath string) bool {
	_, err := os.Stat(confPath + constants.PremergeSuffix())
	return err == nil
}

// MergePersonal is the additive merge: same-name groups merge + new ones append + base kept.
func (e *Editor) MergePersonal(src *Editor, opts MergeOpts) (*MergeReport, error) {
	tmB, tmS := e.topMap(), src.topMap()
	r := &MergeReport{}

	// 1) scalar takeover: ports/secret/controller
	for _, k := range []string{"mixed-port", "port", "socks-port", "secret", "external-controller"} {
		if v := mapGet(tmS, k); v != nil {
			mapSet(tmB, k, deepCopy(v))
			r.Taken = append(r.Taken, k)
		}
	}

	// 2) proxies: append (the base usually has none)
	if v := mapGet(tmS, "proxies"); v != nil && v.Kind == yaml.SequenceNode {
		basePx := mapGet(tmB, "proxies")
		if basePx == nil || basePx.Kind != yaml.SequenceNode {
			basePx = &yaml.Node{Kind: yaml.SequenceNode}
			mapSet(tmB, "proxies", basePx)
		}
		for _, p := range v.Content {
			basePx.Content = append(basePx.Content, deepCopy(p))
			if n := mapGet(p, "name"); n != nil {
				r.PersonalProxies = append(r.PersonalProxies, n.Value)
			}
		}
		r.Taken = append(r.Taken, "proxies (appended)")
	}

	// 3) proxy-groups: same-name merge + new append + base kept
	if v := mapGet(tmS, "proxy-groups"); v != nil && v.Kind == yaml.SequenceNode {
		baseGroups := mapGet(tmB, "proxy-groups")
		if baseGroups == nil || baseGroups.Kind != yaml.SequenceNode {
			baseGroups = &yaml.Node{Kind: yaml.SequenceNode}
			mapSet(tmB, "proxy-groups", baseGroups)
		}

		// index the base groups by name -> node position
		baseIdx := map[string]int{}
		for i, g := range baseGroups.Content {
			if n := mapGet(g, "name"); n != nil {
				baseIdx[n.Value] = i
			}
		}

		// process the personal groups one by one
		for _, pg := range v.Content {
			pn := mapGet(pg, "name")
			if pn == nil {
				continue
			}
			if bi, ok := baseIdx[pn.Value]; ok {
				// same name: field-level merge
				mergeGroupNodes(baseGroups.Content[bi], pg)
				r.GroupsMerged = append(r.GroupsMerged, pn.Value)
			} else {
				// new: append at the end
				baseGroups.Content = append(baseGroups.Content, deepCopy(pg))
				r.GroupsAdded = append(r.GroupsAdded, pn.Value)
			}
		}

		// record the base groups kept (not overridden by personal)
		for _, g := range baseGroups.Content {
			if n := mapGet(g, "name"); n != nil {
				found := false
				for _, m := range r.GroupsMerged {
					if m == n.Value {
						found = true
						break
					}
				}
				if !found {
					r.GroupsKept = append(r.GroupsKept, n.Value)
				}
			}
		}
		r.Taken = append(r.Taken, "proxy-groups (merged)")
	}

	// 4) rules: personal first + base fallback (deduped, MATCH last)
	if v := mapGet(tmS, "rules"); v != nil && v.Kind == yaml.SequenceNode {
		baseRules := mapGet(tmB, "rules")
		var baseList []string
		if baseRules != nil && baseRules.Kind == yaml.SequenceNode {
			for _, rn := range baseRules.Content {
				baseList = append(baseList, rn.Value)
			}
		}

		var merged []string
		seen := map[string]bool{}
		for _, rn := range v.Content {
			if !seen[rn.Value] {
				merged = append(merged, rn.Value)
				seen[rn.Value] = true
			}
		}
		r.RulesPersonal = len(v.Content)

		var matchRule string
		for _, br := range baseList {
			if len(br) > 6 && br[:6] == "MATCH," {
				matchRule = br
				continue
			}
			if !seen[br] {
				merged = append(merged, br)
				seen[br] = true
			} else {
				r.RulesDeduped++
			}
		}
		if matchRule != "" && !seen[matchRule] {
			merged = append(merged, matchRule)
		}
		r.RulesBase = len(baseList)

		newRules := &yaml.Node{Kind: yaml.SequenceNode}
		for _, rs := range merged {
			newRules.Content = append(newRules.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: rs})
		}
		mapSet(tmB, "rules", newRules)
		r.Taken = append(r.Taken, "rules (personal-first + base fallback)")
	}

	// 5) kept (base): mode block / secret mark / infrastructure
	r.Kept = append(r.Kept, "tun/tproxy-port (mode block)", "routing-mark", "dns.listen", "external-ui", "geo*", "ntp", "sniffer", "profile")

	// 6) dns: base by default; with --dns mine the personal side takes over but listen is forced
	if opts.DNSMine {
		if d := mapGet(tmS, "dns"); d != nil {
			dn := deepCopy(d)
			mapSet(tmB, "dns", dn)
			mapSet(dn, "listen", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "[::]:1053"})
			r.Taken = append(r.Taken, "dns (--dns mine, listen forced to [::]:1053)")
		}
	}

	// 7) rule-providers merge (same name: personal wins)
	if rpS := mapGet(tmS, "rule-providers"); rpS != nil && rpS.Kind == yaml.MappingNode {
		rpB := mapGet(tmB, "rule-providers")
		if rpB == nil || rpB.Kind != yaml.MappingNode {
			rpB = &yaml.Node{Kind: yaml.MappingNode}
			mapSet(tmB, "rule-providers", rpB)
		}
		for i := 0; i+1 < len(rpS.Content); i += 2 {
			name := rpS.Content[i].Value
			mapSet(rpB, name, deepCopy(rpS.Content[i+1]))
			r.RuleProvidersAdded = append(r.RuleProvidersAdded, name)
		}
	}

	// 8) proxy-providers merge (same name: base wins)
	if ppS := mapGet(tmS, "proxy-providers"); ppS != nil && ppS.Kind == yaml.MappingNode {
		ppB := mapGet(tmB, "proxy-providers")
		if ppB == nil || ppB.Kind != yaml.MappingNode {
			ppB = &yaml.Node{Kind: yaml.MappingNode}
			mapSet(tmB, "proxy-providers", ppB)
		}
		if ppB.Kind == yaml.MappingNode {
			for i := 0; i+1 < len(ppB.Content); i += 2 {
				r.Providers.BaseKept = append(r.Providers.BaseKept, ppB.Content[i].Value)
			}
		}
		for i := 0; i+1 < len(ppS.Content); i += 2 {
			name := ppS.Content[i].Value
			if mapGet(ppB, name) != nil {
				r.Providers.Conflict = append(r.Providers.Conflict, name)
				continue
			}
			ppB.Content = append(ppB.Content,
				&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: name}, deepCopy(ppS.Content[i+1]))
			r.Providers.Personal = append(r.Providers.Personal, name)
		}
	}

	// 9) placeholder retirement
	ppNow := mapGet(tmB, "proxy-providers")
	var retired []string
	if ppNow != nil && ppNow.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(ppNow.Content); i += 2 {
			pn, pv := ppNow.Content[i].Value, ppNow.Content[i+1]
			if u := mapGet(pv, "url"); u != nil && u.Value == PlaceholderURL {
				if len(ppNow.Content) > 2 {
					retired = append(retired, pn)
				}
			}
		}
		for _, pn := range retired {
			mapDel(ppNow, pn)
		}
	}
	if len(retired) > 0 {
		r.Adjustments = append(r.Adjustments, fmt.Sprintf("removed placeholder subscription %v (real subscription is in place)", retired))
		// Clean every reference to the retired providers: the groups' use lists and the
		// top-level anchor definitions (pr/prd/use) — a merge key's use lives in the
		// anchor definition, not in the group's direct Content.
		cleanupRefs := func(m *yaml.Node) {
			if m == nil || m.Kind != yaml.MappingNode {
				return
			}
			useNode := mapGet(m, "use")
			if useNode == nil || useNode.Kind != yaml.SequenceNode {
				return
			}
			var keep []*yaml.Node
			for _, u := range useNode.Content {
				isRetired := false
				for _, rn := range retired {
					if u.Value == rn {
						isRetired = true
						break
					}
				}
				if !isRetired {
					keep = append(keep, u)
				}
			}
			useNode.Content = keep
		}
		// clean the groups (direct use lists)
		gl := mapGet(tmB, "proxy-groups")
		if gl != nil && gl.Kind == yaml.SequenceNode {
			for _, g := range gl.Content {
				cleanupRefs(g)
			}
		}
		// clean the top-level anchor definitions (the use lists inside pr/prd/use)
		for _, anchor := range []string{"pr", "prd", "use"} {
			cleanupRefs(mapGet(tmB, anchor))
		}
	}

	// 10) process-based routing
	hasProcess := false
	if rules := mapGet(tmB, "rules"); rules != nil && rules.Kind == yaml.SequenceNode {
		for _, rule := range rules.Content {
			if len(rule.Value) >= 8 && rule.Value[:8] == "PROCESS-" {
				hasProcess = true
				break
			}
		}
	}
	if hasProcess {
		mapSet(tmB, "find-process-mode", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "strict"})
		r.Adjustments = append(r.Adjustments, "find-process-mode → strict (PROCESS- rule detected)")
	}

	return r, nil
}

// mergeGroupNodes merges same-name groups at field level: personal fields override/add,
// proxies/use become a union.
func mergeGroupNodes(base, personal *yaml.Node) {
	if base == nil || personal == nil || base.Kind != yaml.MappingNode || personal.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i+1 < len(personal.Content); i += 2 {
		key := personal.Content[i].Value
		val := personal.Content[i+1]

		if key == "proxies" || key == "use" {
			baseVal := mapGet(base, key)
			if baseVal == nil || baseVal.Kind != yaml.SequenceNode {
				base.Content = append(base.Content, personal.Content[i], deepCopy(val))
				continue
			}
			// union: personal first (higher priority), the base's original entries
			// appended after, deduplicated
			added := map[string]bool{}
			var newList []*yaml.Node
			for _, pv := range val.Content {
				if !added[pv.Value] {
					newList = append(newList, deepCopy(pv))
					added[pv.Value] = true
				}
			}
			for _, bv := range baseVal.Content {
				if !added[bv.Value] {
					newList = append(newList, bv)
					added[bv.Value] = true
				}
			}
			baseVal.Content = newList
		} else {
			mapSet(base, key, deepCopy(val))
		}
	}
}

// WireAfterMerge wires references after the merge.
func (e *Editor) WireAfterMerge(baseProviders, personalProxies []string, opts MergeOpts) (wired int) {
	if !opts.NoWire {
		for _, pn := range baseProviders {
			if !e.providerReferenced(pn) {
				wired += e.WireProvider(pn, true, nil)
			}
		}
	}
	if !opts.NoProxyWire {
		gl := mapGet(e.topMap(), "proxy-groups")
		if gl == nil || gl.Kind != yaml.SequenceNode {
			return
		}
		for _, g := range gl.Content {
			px := mapGet(g, "proxies")
			if px == nil || px.Kind != yaml.SequenceNode || len(px.Content) == 0 {
				continue
			}
			for _, n := range personalProxies {
				seqAppend(px, n)
			}
		}
	}
	return
}

// providerReferenced reports whether any group's use list already references the provider.
func (e *Editor) providerReferenced(name string) bool {
	gl := mapGet(e.topMap(), "proxy-groups")
	if gl == nil || gl.Kind != yaml.SequenceNode {
		return false
	}
	for _, g := range gl.Content {
		if use := mapGet(g, "use"); use != nil && use.Kind == yaml.SequenceNode {
			for _, u := range use.Content {
				if u.Value == name {
					return true
				}
			}
		}
	}
	return false
}

// deepCopy deep-copies a yaml node.
func deepCopy(n *yaml.Node) *yaml.Node {
	if n == nil {
		return nil
	}
	c := *n
	c.Content = nil
	for _, ch := range n.Content {
		c.Content = append(c.Content, deepCopy(ch))
	}
	return &c
}
