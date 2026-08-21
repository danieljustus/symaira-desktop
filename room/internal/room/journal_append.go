package room

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/danieljustus/symaira-room/internal/event"
	"github.com/danieljustus/symaira-room/internal/identity"
	"github.com/danieljustus/symaira-room/internal/members"
)

var appendMu sync.Mutex

type JournalStats struct {
	LastSeq     uint64
	LastHash    string
	MaxLamport  uint64
	MemberState *members.State
}

func ReadJournalStats(roomDir string) (*JournalStats, error) {
	journalDir := filepath.Join(roomDir, "journal")
	entries, err := os.ReadDir(journalDir)
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("read journal dir: %w", err)
	}

	state := members.NewState()
	var maxLamport uint64

	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".jsonl") {
			filePath := filepath.Join(journalDir, entry.Name())
			f, err := os.Open(filePath)
			if err != nil {
				continue
			}
			scanner := bufio.NewScanner(f)
			for scanner.Scan() {
				line := scanner.Bytes()
				if len(strings.TrimSpace(string(line))) == 0 {
					continue
				}
				ev, err := event.UnmarshalJSONLine(line)
				if err != nil {
					continue
				}
				if ev.Lamport > maxLamport {
					maxLamport = ev.Lamport
				}
				_ = state.ApplyEvent(ev)
			}
			_ = f.Close()
		}
	}

	return &JournalStats{
		MaxLamport:  maxLamport,
		MemberState: state,
	}, nil
}

func GetAuthorStats(roomDir, authorID string) (seq uint64, prevHash string, err error) {
	journalFile := filepath.Join(roomDir, "journal", authorID+".jsonl")
	f, err := os.Open(journalFile)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, "sha256:0000000000000000000000000000000000000000000000000000000000000000", nil
		}
		return 0, "", err
	}
	defer func() { _ = f.Close() }()

	var lastLine []byte
	var count uint64
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(strings.TrimSpace(string(line))) > 0 {
			count++
			lastLine = append([]byte(nil), line...)
		}
	}

	if count == 0 {
		return 0, "sha256:0000000000000000000000000000000000000000000000000000000000000000", nil
	}

	h := sha256.Sum256(lastLine)
	return count, "sha256:" + hex.EncodeToString(h[:]), nil
}

func AppendEvent(roomDir string, ev *event.Event) error {
	appendMu.Lock()
	defer appendMu.Unlock()

	journalDir := filepath.Join(roomDir, "journal")
	if err := os.MkdirAll(journalDir, 0755); err != nil {
		return fmt.Errorf("mkdir journal: %w", err)
	}

	line, err := ev.MarshalJSONLine()
	if err != nil {
		return fmt.Errorf("marshal event line: %w", err)
	}

	journalFile := filepath.Join(journalDir, ev.Author+".jsonl")
	f, err := os.OpenFile(journalFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("open journal file: %w", err)
	}
	defer func() { _ = f.Close() }()

	if _, err := f.Write(line); err != nil {
		return fmt.Errorf("write event line: %w", err)
	}
	return nil
}

func ReadRoomConfig(roomDir string) (*RoomConfig, error) {
	data, err := os.ReadFile(filepath.Join(roomDir, "room.toml"))
	if err != nil {
		return nil, fmt.Errorf("read room.toml: %w", err)
	}
	// Parse basic key=value from room.toml
	var cfg RoomConfig
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			k := strings.TrimSpace(parts[0])
			v := strings.Trim(strings.TrimSpace(parts[1]), `"`)
			switch k {
			case "id":
				cfg.ID = v
			case "created":
				cfg.Created = v
			case "root_pubkey":
				cfg.RootPubkey = v
			case "root_event":
				cfg.RootEvent = v
			}
		}
	}
	return &cfg, nil
}

func PostNote(roomDir string, text string, id *identity.Identity) (*event.Event, error) {
	roomCfg, err := ReadRoomConfig(roomDir)
	if err != nil {
		return nil, err
	}

	stats, err := ReadJournalStats(roomDir)
	if err != nil {
		return nil, err
	}

	member, exists := stats.MemberState.Members[id.MemberID]
	if exists && member.Role == members.RoleObserver {
		return nil, members.ErrObserverForbidden
	}

	lastSeq, prevHash, err := GetAuthorStats(roomDir, id.MemberID)
	if err != nil {
		return nil, err
	}

	bodyMap := map[string]string{"text": text}
	bodyBytes, _ := json.Marshal(bodyMap)

	ev := &event.Event{
		V:       event.CurrentVersion,
		ID:      GenerateEventID(),
		Room:    roomCfg.ID,
		Author:  id.MemberID,
		Seq:     lastSeq + 1,
		Prev:    prevHash,
		Lamport: stats.MaxLamport + 1,
		TS:      event.FormatTimestamp(time.Now()),
		Kind:    event.KindNotePosted,
		Body:    json.RawMessage(bodyBytes),
	}

	if err := ev.Sign(id); err != nil {
		return nil, fmt.Errorf("sign note event: %w", err)
	}

	if err := AppendEvent(roomDir, ev); err != nil {
		return nil, fmt.Errorf("append event: %w", err)
	}

	return ev, nil
}

func RecordDecision(roomDir string, text string, refs []string, id *identity.Identity) (*event.Event, error) {
	roomCfg, err := ReadRoomConfig(roomDir)
	if err != nil {
		return nil, err
	}

	stats, err := ReadJournalStats(roomDir)
	if err != nil {
		return nil, err
	}

	member, exists := stats.MemberState.Members[id.MemberID]
	if exists && member.Role == members.RoleObserver {
		return nil, members.ErrObserverForbidden
	}

	lastSeq, prevHash, err := GetAuthorStats(roomDir, id.MemberID)
	if err != nil {
		return nil, err
	}

	if refs == nil {
		refs = []string{}
	}
	bodyMap := map[string]any{"text": text, "refs": refs}
	bodyBytes, _ := json.Marshal(bodyMap)

	ev := &event.Event{
		V:       event.CurrentVersion,
		ID:      GenerateEventID(),
		Room:    roomCfg.ID,
		Author:  id.MemberID,
		Seq:     lastSeq + 1,
		Prev:    prevHash,
		Lamport: stats.MaxLamport + 1,
		TS:      event.FormatTimestamp(time.Now()),
		Kind:    event.KindDecisionRecorded,
		Body:    json.RawMessage(bodyBytes),
	}

	if err := ev.Sign(id); err != nil {
		return nil, fmt.Errorf("sign decision event: %w", err)
	}

	if err := AppendEvent(roomDir, ev); err != nil {
		return nil, fmt.Errorf("append event: %w", err)
	}

	return ev, nil
}
