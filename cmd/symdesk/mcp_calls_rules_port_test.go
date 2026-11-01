package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Classification-rule tools: desk_rules_list/add/update/delete and the legacy
// aliases list_rules/add_rule/delete_rule.
func mcpCallsRulesCases() []mcpCallsCase {
	var cases []mcpCallsCase
	args := func(v map[string]any) string {
		data, err := json.Marshal(v)
		if err != nil {
			panic(err)
		}
		return string(data)
	}
	// Decoder matrix for every rule tool, in write mode and (for the write
	// tools) in read-only mode, where Go answers "Unknown tool".
	decode := []string{``, `null`, `{}`, `[]`, `"s"`, `7`, `true`, `{"pattern":5}`, `{"pattern":"p","kind":false,"value":"v"}`, `{"id":"x"}`, `{"id":1.5}`, `{"id":1e3}`, `{"id":9223372036854775808}`, `{"id":-1}`, `{"id":0}`, `{"id":null,"pattern":null}`, `{"ID":1,"PATTERN":"p","Kind":"tag","VALUE":"v"}`, `{"pattern":"p","pattern":"q","kind":"tag","value":"v"}`, `{"id":1,"id":"x"}`, `{"pattern":"p","kind":"tag","value":"v","extra":{"deep":[1,2]}}`, `{"pattern":"\ud800","kind":"tag","value":"v"}`, `{"id":1,"pattern":"p","kind":"tag","value":"v"} x`}
	for _, name := range []string{"desk_rules_list", "list_rules", "desk_rules_add", "add_rule", "desk_rules_update", "desk_rules_delete", "delete_rule"} {
		write := name != "desk_rules_list" && name != "list_rules"
		for i, a := range decode {
			cases = append(cases, mcpCallsCase1(fmt.Sprintf("%s-decode-%02d", name, i), true, name, a))
		}
		cases = append(cases, mcpCallsCase1(name+"-read-only", false, name, `{"id":1,"pattern":"p","kind":"tag","value":"v"}`))
		if write {
			cases = append(cases, mcpCallsCase1(name+"-read-only-empty", false, name, ``))
		}
	}
	// Required-field and validation errors.
	for i, a := range []string{`{"pattern":"p","kind":"tag"}`, `{"pattern":"p","value":"v"}`, `{"kind":"tag","value":"v"}`, `{"pattern":"p","kind":"nope","value":"v"}`, `{"pattern":"p","kind":"CATEGORY","value":"v"}`, `{"pattern":" ","kind":"tag","value":"v"}`, `{"pattern":"p","kind":"tag","value":" "}`, `{"pattern":"p","kind":"correspondent","value":"v"}`, `{"pattern":"p","kind":"document_type","value":"v"}`, `{"pattern":"p","kind":"category","value":"v"}`} {
		cases = append(cases, mcpCallsCase1(fmt.Sprintf("add-validation-%02d", i), true, "desk_rules_add", a))
	}
	// Sequences against one persistent store: each step is its own process.
	seq := func(id string, allowWrite bool, steps ...[2]string) {
		c := mcpCallsCase{ID: id, AllowWrite: allowWrite}
		for _, s := range steps {
			c.Steps = append(c.Steps, mcpCallsStep{Request: mcpCallsRequest(s[0], s[1])})
		}
		cases = append(cases, c)
	}
	add := func(pattern, kind, value string) [2]string {
		return [2]string{"desk_rules_add", args(map[string]any{"pattern": pattern, "kind": kind, "value": value})}
	}
	list := [2]string{"desk_rules_list", ``}
	seq("rules-empty-list", false, list)
	seq("rules-add-list", true, list, add("invoice", "category", "Finance"), add("Tax", "tag", "tax"), list)
	seq("rules-add-unicode-html", true, add("Größe <&> 日本語", "correspondent", "A & B <c>"), list)
	seq("rules-add-duplicate", true, add("dup", "tag", "x"), add("dup", "tag", "x"), list)
	seq("rules-add-alias-list-alias", true, [2]string{"add_rule", args(map[string]any{"pattern": "alias", "kind": "tag", "value": "a"})}, [2]string{"list_rules", ``}, list)
	seq("rules-list-read-only-after-write", true, add("seen", "tag", "s"))
	cases[len(cases)-1].Steps = append(cases[len(cases)-1].Steps, mcpCallsStep{Request: mcpCallsRequest("list_rules", ``)})
	seq("rules-update", true, add("old", "tag", "o"), [2]string{"desk_rules_update", args(map[string]any{"id": 1, "pattern": "new", "kind": "category", "value": "n"})}, list)
	seq("rules-update-missing", true, [2]string{"desk_rules_update", args(map[string]any{"id": 42, "pattern": "new", "kind": "category", "value": "n"})})
	seq("rules-update-invalid-kind", true, add("old", "tag", "o"), [2]string{"desk_rules_update", args(map[string]any{"id": 1, "pattern": "new", "kind": "bogus", "value": "n"})}, list)
	seq("rules-update-required", true, [2]string{"desk_rules_update", args(map[string]any{"id": 1, "pattern": "new"})}, [2]string{"desk_rules_update", args(map[string]any{"pattern": "new", "kind": "tag", "value": "v"})})
	seq("rules-delete", true, add("gone", "tag", "g"), add("stay", "tag", "s"), [2]string{"desk_rules_delete", `{"id":1}`}, list)
	seq("rules-delete-alias", true, add("gone", "tag", "g"), [2]string{"delete_rule", `{"id":1}`}, list)
	seq("rules-delete-missing", true, [2]string{"desk_rules_delete", `{"id":42}`}, list)
	seq("rules-delete-twice", true, add("gone", "tag", "g"), [2]string{"desk_rules_delete", `{"id":1}`}, [2]string{"desk_rules_delete", `{"id":1}`})
	seq("rules-id-after-delete", true, add("a", "tag", "1"), add("b", "tag", "2"), [2]string{"desk_rules_delete", `{"id":2}`}, add("c", "tag", "3"), list)
	long := strings.Repeat("x", 5000)
	seq("rules-long-values", true, add(long, "tag", long), list)
	// No configured vault, framing and notification variants.
	nv := mcpCallsCase1("rules-add-no-vault", true, "desk_rules_add", args(map[string]any{"pattern": "p", "kind": "tag", "value": "v"}))
	nv.NoVault = true
	cases = append(cases, nv)
	nvl := mcpCallsCase1("rules-list-no-vault", false, "desk_rules_list", ``)
	nvl.NoVault = true
	cases = append(cases, nvl)
	framed := mcpCallsCase1("rules-add-framed", true, "desk_rules_add", args(map[string]any{"pattern": "p", "kind": "tag", "value": "v"}))
	framed.Steps[0].Framed = true
	cases = append(cases, framed)
	notification := mcpCallsCase1("rules-add-notification", true, "desk_rules_add", args(map[string]any{"pattern": "p", "kind": "tag", "value": "v"}))
	var request map[string]any
	if err := json.Unmarshal([]byte(notification.Steps[0].Request), &request); err != nil {
		panic(err)
	}
	delete(request, "id")
	raw, _ := json.Marshal(request)
	notification.Steps = append(notification.Steps, mcpCallsStep{Request: mcpCallsRequest("desk_rules_list", ``)})
	notification.Steps[0].Request = string(raw)
	cases = append(cases, notification)
	return cases
}
