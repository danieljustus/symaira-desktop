package members

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/danieljustus/symaira-desktop/internal/room/event"
	"github.com/danieljustus/symaira-desktop/internal/room/identity"
)

type Role string
type MemberKind string
type Action string

const (
	RoleOwner    Role = "owner"
	RoleMember   Role = "member"
	RoleAgent    Role = "agent"
	RoleObserver Role = "observer"

	KindHuman MemberKind = "human"
	KindAgent MemberKind = "agent"

	ActionManageMembers Action = "manage_members"
	ActionApprove       Action = "approve"
	ActionPostLink      Action = "post_link"
	ActionRequestRun    Action = "request_run"
)

var (
	ErrUnauthorizedOwnerAction = errors.New("only room owners can perform member management")
	ErrAgentApprovalForbidden  = errors.New("agent role is strictly forbidden from signing approval events")
	ErrObserverForbidden       = errors.New("observer role has read-only access")
	ErrMemberNotFound          = errors.New("member not found")
	ErrInvalidRole             = errors.New("invalid member role")
	ErrInvalidKind             = errors.New("invalid member kind")
)

type Member struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	PublicKey ed25519.PublicKey `json:"public_key"`
	Role      Role              `json:"role"`
	Kind      MemberKind        `json:"kind"`
}

type State struct {
	Members map[string]*Member `json:"members"`
}

func NewState() *State {
	return &State{
		Members: make(map[string]*Member),
	}
}

func (m *Member) CanPerform(act Action) bool {
	switch m.Role {
	case RoleOwner:
		return true
	case RoleMember:
		return act == ActionApprove || act == ActionPostLink || act == ActionRequestRun
	case RoleAgent:
		// Hard invariant: Agent can NEVER approve
		return act == ActionPostLink || act == ActionRequestRun
	case RoleObserver:
		return false
	default:
		return false
	}
}

func (s *State) ApplyEvent(e *event.Event) error {
	switch e.Kind {
	case event.KindRoomCreated:
		var body struct {
			Name      string `json:"name"`
			PublicKey string `json:"public_key"`
		}
		if err := json.Unmarshal(e.Body, &body); err != nil {
			return fmt.Errorf("unmarshal room.created body: %w", err)
		}
		pubBytes, err := hex.DecodeString(body.PublicKey)
		if err != nil || len(pubBytes) != ed25519.PublicKeySize {
			return fmt.Errorf("invalid root pubkey: %w", err)
		}
		s.Members[e.Author] = &Member{
			ID:        e.Author,
			Name:      body.Name,
			PublicKey: ed25519.PublicKey(pubBytes),
			Role:      RoleOwner,
			Kind:      KindHuman,
		}

	case event.KindMemberAdded:
		// Must be signed by an owner
		author, exists := s.Members[e.Author]
		if !exists || author.Role != RoleOwner {
			return ErrUnauthorizedOwnerAction
		}

		var body struct {
			ID        string     `json:"id"`
			Name      string     `json:"name"`
			PublicKey string     `json:"public_key"`
			Role      Role       `json:"role"`
			Kind      MemberKind `json:"kind"`
		}
		if err := json.Unmarshal(e.Body, &body); err != nil {
			return fmt.Errorf("unmarshal member.added body: %w", err)
		}
		pubBytes, err := hex.DecodeString(body.PublicKey)
		if err != nil || len(pubBytes) != ed25519.PublicKeySize {
			return fmt.Errorf("invalid member pubkey: %w", err)
		}

		s.Members[body.ID] = &Member{
			ID:        body.ID,
			Name:      body.Name,
			PublicKey: ed25519.PublicKey(pubBytes),
			Role:      body.Role,
			Kind:      body.Kind,
		}

	case event.KindMemberRemoved:
		author, exists := s.Members[e.Author]
		if !exists || author.Role != RoleOwner {
			return ErrUnauthorizedOwnerAction
		}
		var body struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(e.Body, &body); err != nil {
			return fmt.Errorf("unmarshal member.removed body: %w", err)
		}
		delete(s.Members, body.ID)

	case event.KindMemberRoleChanged:
		author, exists := s.Members[e.Author]
		if !exists || author.Role != RoleOwner {
			return ErrUnauthorizedOwnerAction
		}
		var body struct {
			ID   string `json:"id"`
			Role Role   `json:"role"`
		}
		if err := json.Unmarshal(e.Body, &body); err != nil {
			return fmt.Errorf("unmarshal member.role_changed body: %w", err)
		}
		if target, ok := s.Members[body.ID]; ok {
			target.Role = body.Role
		}

	case event.KindRunApproved, event.KindCheckpointResolved:
		// Hard invariant check: Agent or observer cannot sign approvals
		author, exists := s.Members[e.Author]
		if !exists {
			return ErrMemberNotFound
		}
		if author.Role == RoleAgent {
			return ErrAgentApprovalForbidden
		}
		if author.Role == RoleObserver {
			return ErrObserverForbidden
		}
	}
	return nil
}

// ApplySignedEvent authenticates membership changes against the current
// membership key before applying them. The first room.created event is
// self-authenticating only when its author matches the public key's member ID.
func (s *State) ApplySignedEvent(e *event.Event) error {
	var publicKey ed25519.PublicKey
	if e.Kind == event.KindRoomCreated {
		if len(s.Members) != 0 {
			return errors.New("room.created must be the first membership event")
		}
		var body struct {
			PublicKey string `json:"public_key"`
		}
		if err := json.Unmarshal(e.Body, &body); err != nil {
			return fmt.Errorf("unmarshal room.created body: %w", err)
		}
		decoded, err := hex.DecodeString(body.PublicKey)
		if err != nil {
			return fmt.Errorf("invalid root pubkey: %w", err)
		}
		if len(decoded) != ed25519.PublicKeySize {
			return errors.New("invalid root pubkey: wrong size")
		}
		publicKey = ed25519.PublicKey(decoded)
		if identity.ComputeMemberID(publicKey) != e.Author {
			return errors.New("room.created author does not match its public key")
		}
	} else {
		author, exists := s.Members[e.Author]
		if !exists {
			return ErrMemberNotFound
		}
		publicKey = author.PublicKey
	}
	if err := e.VerifySignature(publicKey); err != nil {
		return err
	}
	return s.ApplyEvent(e)
}
