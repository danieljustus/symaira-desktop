package members

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/room/event"
)

const membershipFixture = "testdata/port/room/membership.json"

type membershipCase struct {
	ID      string          `json:"id"`
	Kind    string          `json:"kind"`
	Author  string          `json:"author"`
	Body    json.RawMessage `json:"body"`
	Error   string          `json:"error"`
	Members []memberView    `json:"members"`
}

type memberView struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	PublicKey string     `json:"public_key"`
	Role      Role       `json:"role"`
	Kind      MemberKind `json:"kind"`
}

type permissionCase struct {
	Role    Role   `json:"role"`
	Action  Action `json:"action"`
	Allowed bool   `json:"allowed"`
}

type membershipContract struct {
	SchemaVersion int               `json:"schema_version"`
	SourceHashes  map[string]string `json:"source_hashes"`
	Permissions   []permissionCase  `json:"permissions"`
	Transitions   []membershipCase  `json:"transitions"`
}

func TestPortMembershipContract(t *testing.T) {
	root := filepath.Clean(filepath.Join(filepath.Dir(membershipSourcePath(t)), "../../.."))
	fixture := membershipContract{SchemaVersion: 1, SourceHashes: map[string]string{}}
	for _, rel := range []string{"internal/room/members/members.go", "internal/room/members/port_membership_contract_test.go"} {
		data, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		fixture.SourceHashes[rel] = hex.EncodeToString(sum[:])
	}
	for _, role := range []Role{RoleOwner, RoleMember, RoleAgent, RoleObserver, "unknown"} {
		for _, action := range []Action{ActionManageMembers, ActionApprove, ActionPostLink, ActionRequestRun, "unknown"} {
			fixture.Permissions = append(fixture.Permissions, permissionCase{role, action, (&Member{Role: role}).CanPerform(action)})
		}
	}
	state := NewState()
	ownerKey := "0000000000000000000000000000000000000000000000000000000000000000"
	agentKey := "1111111111111111111111111111111111111111111111111111111111111111"
	observerKey := "2222222222222222222222222222222222222222222222222222222222222222"
	steps := []struct{ id, kind, author, body string }{
		{"owner-create", event.KindRoomCreated, "owner", `{"name":"Room","public_key":"` + ownerKey + `"}`},
		{"agent-add", event.KindMemberAdded, "owner", `{"id":"agent","name":"Bot","public_key":"` + agentKey + `","role":"member","Role":"agent","kind":"agent"}`},
		{"agent-cannot-add", event.KindMemberAdded, "agent", `{"id":"intruder","public_key":"` + observerKey + `"}`},
		{"agent-cannot-approve", event.KindRunApproved, "agent", `{}`},
		{"unknown-cannot-approve", event.KindCheckpointResolved, "absent", `{}`},
		{"observer-add", event.KindMemberAdded, "owner", `{"id":"observer","name":"Observer","public_key":"` + observerKey + `","role":"observer","kind":"human"}`},
		{"observer-cannot-resolve", event.KindCheckpointResolved, "observer", `{}`},
		{"observer-cannot-approve", event.KindRunApproved, "observer", `{}`},
		{"observer-promote", event.KindMemberRoleChanged, "owner", `{"id":"observer","role":"member"}`},
		{"member-approves", event.KindRunApproved, "observer", `{}`},
		{"non-owner-cannot-remove", event.KindMemberRemoved, "observer", `{"id":"agent"}`},
		{"agent-role-duplicate-null", event.KindMemberRoleChanged, "owner", `{"id":"agent","role":"agent","Role":null}`},
		{"agent-still-cannot-approve", event.KindRunApproved, "agent", `{}`},
		{"remove-agent", event.KindMemberRemoved, "owner", `{"ID":"agent","name":123}`},
		{"removed-agent-approval", event.KindRunApproved, "agent", `{}`},
		{"invalid-key-length", event.KindMemberAdded, "owner", `{"id":"bad","public_key":"00"}`},
		{"invalid-key-odd-hex", event.KindMemberAdded, "owner", `{"id":"bad","public_key":"0"}`},
		{"invalid-key-odd-nonhex", event.KindMemberAdded, "owner", `{"id":"bad","public_key":"g"}`},
		{"invalid-key-nonhex", event.KindMemberAdded, "owner", `{"id":"bad","public_key":"gg"}`},
		{"invalid-root-key-odd-hex", event.KindRoomCreated, "other", `{"public_key":"0"}`},
		{"invalid-root-key-odd-nonhex", event.KindRoomCreated, "other", `{"public_key":"g"}`},
		{"null-fields-are-empty", event.KindMemberAdded, "owner", `{"id":"nullish","name":null,"public_key":"` + ownerKey + `","role":null,"kind":null}`},
		{"null-role-change-body", event.KindMemberRoleChanged, "owner", `null`},
		{"blank-id-add", event.KindMemberAdded, "owner", `{"id":"","name":"Blank","public_key":"` + ownerKey + `"}`},
		{"null-removes-blank-id", event.KindMemberRemoved, "owner", `null`},
		{"unknown-event-noop", event.KindNotePosted, "owner", `{}`},
	}
	for _, step := range steps {
		err := state.ApplyEvent(&event.Event{Kind: step.kind, Author: step.author, Body: json.RawMessage(step.body)})
		if step.id == "observer-add" && (err != nil || state.Members["observer"] == nil || state.Members["observer"].Role != RoleObserver) {
			t.Fatal("observer fixture must install an observer before approval checks")
		}
		if step.id == "agent-role-duplicate-null" && (err != nil || state.Members["agent"] == nil || state.Members["agent"].Role != RoleAgent) {
			t.Fatal("duplicate null must not clear agent role")
		}
		result := membershipCase{ID: step.id, Kind: step.kind, Author: step.author, Body: json.RawMessage(step.body), Members: []memberView{}}
		if err != nil {
			result.Error = err.Error()
		}
		for _, m := range state.Members {
			result.Members = append(result.Members, memberView{m.ID, m.Name, hex.EncodeToString(m.PublicKey), m.Role, m.Kind})
		}
		sort.Slice(result.Members, func(i, j int) bool { return result.Members[i].ID < result.Members[j].ID })
		fixture.Transitions = append(fixture.Transitions, result)
	}
	data, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	path := filepath.Join(root, membershipFixture)
	if os.Getenv("PORT_GENERATE") == "1" {
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(current) != string(data) {
		t.Fatal("Go membership fixture drift: regenerate explicitly")
	}
	if len(fixture.Permissions) != 25 || len(fixture.Transitions) != 26 {
		t.Fatal("membership case inventory changed")
	}
}

func membershipSourcePath(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller path unavailable")
	}
	return file
}
