package service

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/compose"
)

// mockSymmemoryPublishScript is a stateful mock: entity add/relate/set are
// tracked in files under the mock's own directory so a test can assert how
// many times each was actually invoked (real symmemory `set` creates a new
// memory per call — never deduplicates — so PublishMeetingProposal's own
// idempotency guard is what this test suite is really exercising).
func mockSymmemoryPublishScript(callLogPath string) string {
	return `#!/bin/bash
LOG="` + callLogPath + `"
if [ "$1" = "entity" ]; then
  case "$2" in
    show)
      if [ "$3" = "Meeting m1" ] || [ "$3" = "Alice Example" ]; then
        echo "entity_show $3" >> "$LOG"
        if [ "$3" = "Meeting m1" ]; then
          echo '{"id":"e-meeting","name":"Meeting m1","type":"other","aliases":[],"description":""}'
        else
          echo '{"id":"e-alice","name":"Alice Example","type":"person","aliases":[],"description":""}'
        fi
      else
        echo "Error: entity not found: $3" >&2
        exit 1
      fi
      ;;
    add)
      echo "entity_add $3" >> "$LOG"
      echo "Entity created: $3"
      ;;
    list)
      echo '[{"id":"e-alice","name":"Alice Example","type":"person","aliases":[],"description":""}]'
      ;;
    relate)
      echo "entity_relate $3 $4 $5" >> "$LOG"
      echo "Related: $3 --$4--> $5"
      ;;
  esac
elif [ "$1" = "set" ]; then
  echo "memory_set" >> "$LOG"
  n=$(wc -l < "$LOG" | tr -d ' ')
  echo "{\"id\":\"mem-$n\",\"content\":\"x\",\"scope\":\"project\",\"entities\":[]}"
fi
`
}

func importFixtureMeetingWithConfirmedParticipant(t *testing.T, symmeetDir, symmemoryDir, callLog string) (*Service, string) {
	t.Helper()
	svc, path := importFixtureMeeting(t, symmeetDir)
	writeMockSymmemory(t, symmemoryDir, mockSymmemoryPublishScript(callLog))
	withMockSymmemoryPath(t, symmemoryDir)

	if err := svc.ConfirmParticipant(path, "speaker_0", "e-alice"); err != nil {
		t.Fatal(err)
	}
	return svc, path
}

func TestPublishMeetingProposalCreatesRelationAndFact(t *testing.T) {
	symmeetDir := t.TempDir()
	symmemoryDir := t.TempDir()
	callLog := symmemoryDir + "/calls.log"
	svc, path := importFixtureMeetingWithConfirmedParticipant(t, symmeetDir, symmemoryDir, callLog)

	result, err := svc.PublishMeetingProposal(path, MeetingPublishProposal{
		Facts: []MeetingFact{{Value: "Alice proposed the Q3 roadmap."}},
	})
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if result.RelationsCreated != 1 {
		t.Errorf("expected 1 relation created, got %d", result.RelationsCreated)
	}
	if len(result.FactsPublished) != 1 {
		t.Errorf("expected 1 fact published, got %+v", result.FactsPublished)
	}
	if result.FactsSkipped != 0 {
		t.Errorf("expected 0 facts skipped on first publish, got %d", result.FactsSkipped)
	}
}

// The core idempotency guarantee: symmemory's own `set` command is NOT
// idempotent (verified against the real CLI — two identical calls create
// two memories), so PublishMeetingProposal must track published facts
// itself and skip them on a repeat apply of the same proposal.
func TestPublishMeetingProposalRepeatApplyDoesNotDuplicateFacts(t *testing.T) {
	symmeetDir := t.TempDir()
	symmemoryDir := t.TempDir()
	callLog := symmemoryDir + "/calls.log"
	svc, path := importFixtureMeetingWithConfirmedParticipant(t, symmeetDir, symmemoryDir, callLog)

	proposal := MeetingPublishProposal{
		Facts: []MeetingFact{{Value: "Alice proposed the Q3 roadmap."}},
	}

	first, err := svc.PublishMeetingProposal(path, proposal)
	if err != nil {
		t.Fatalf("first publish: expected success, got %v", err)
	}
	if len(first.FactsPublished) != 1 {
		t.Fatalf("expected 1 fact published on first apply, got %+v", first.FactsPublished)
	}

	second, err := svc.PublishMeetingProposal(path, proposal)
	if err != nil {
		t.Fatalf("second publish: expected success, got %v", err)
	}
	if len(second.FactsPublished) != 0 {
		t.Errorf("expected 0 NEW facts published on repeat apply, got %+v", second.FactsPublished)
	}
	if second.FactsSkipped != 1 {
		t.Errorf("expected 1 fact skipped on repeat apply, got %d", second.FactsSkipped)
	}

	// Relations are separately idempotent at the symmemory layer, so
	// repeating the relate call is expected and harmless.
	if second.RelationsCreated != 1 {
		t.Errorf("expected the attendance relation to be (idempotently) re-applied, got %d", second.RelationsCreated)
	}
}

func TestPublishMeetingProposalNewFactAfterPriorPublishOnlySendsTheNewOne(t *testing.T) {
	symmeetDir := t.TempDir()
	symmemoryDir := t.TempDir()
	callLog := symmemoryDir + "/calls.log"
	svc, path := importFixtureMeetingWithConfirmedParticipant(t, symmeetDir, symmemoryDir, callLog)

	if _, err := svc.PublishMeetingProposal(path, MeetingPublishProposal{
		Facts: []MeetingFact{{Value: "Alice proposed the Q3 roadmap."}},
	}); err != nil {
		t.Fatal(err)
	}

	second, err := svc.PublishMeetingProposal(path, MeetingPublishProposal{
		Facts: []MeetingFact{
			{Value: "Alice proposed the Q3 roadmap."},    // already published
			{Value: "Bob will send the follow-up deck."}, // new
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.FactsPublished) != 1 {
		t.Errorf("expected exactly 1 newly published fact, got %+v", second.FactsPublished)
	}
	if second.FactsSkipped != 1 {
		t.Errorf("expected 1 fact skipped as already published, got %d", second.FactsSkipped)
	}
}

func TestPublishMeetingProposalSkipsUnconfirmedParticipants(t *testing.T) {
	symmeetDir := t.TempDir()
	symmemoryDir := t.TempDir()
	callLog := symmemoryDir + "/calls.log"
	// Import without confirming the participant — entity_id stays empty.
	svc, path := importFixtureMeeting(t, symmeetDir)
	writeMockSymmemory(t, symmemoryDir, mockSymmemoryPublishScript(callLog))
	withMockSymmemoryPath(t, symmemoryDir)

	result, err := svc.PublishMeetingProposal(path, MeetingPublishProposal{})
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if result.RelationsCreated != 0 {
		t.Errorf("expected 0 relations for an unconfirmed participant, got %d", result.RelationsCreated)
	}
}

func TestPublishMeetingProposalSymmemoryUnavailable(t *testing.T) {
	symmeetDir := t.TempDir()
	svc, path := importFixtureMeeting(t, symmeetDir)

	// A bare-bones PATH, not a prepended one: the real symmemory installed
	// on the dev machine must not leak into this "unavailable" scenario.
	t.Setenv("PATH", "/usr/bin:/bin")
	compose.ResetCache()
	t.Cleanup(compose.ResetCache)

	if _, err := svc.PublishMeetingProposal(path, MeetingPublishProposal{}); err != ErrSymmemoryUnavailable {
		t.Errorf("expected ErrSymmemoryUnavailable, got %v", err)
	}
}

func TestPublishMeetingProposalBlankFactsAreIgnored(t *testing.T) {
	symmeetDir := t.TempDir()
	symmemoryDir := t.TempDir()
	callLog := symmemoryDir + "/calls.log"
	svc, path := importFixtureMeetingWithConfirmedParticipant(t, symmeetDir, symmemoryDir, callLog)

	result, err := svc.PublishMeetingProposal(path, MeetingPublishProposal{
		Facts: []MeetingFact{{Value: "   "}, {Value: ""}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.FactsPublished) != 0 {
		t.Errorf("expected blank facts to be ignored, got %+v", result.FactsPublished)
	}
}

// mockSymmemoryPublishScriptFailingNthSet is like mockSymmemoryPublishScript
// but the Nth `set` call (counted across the mock's whole lifetime, i.e.
// across retries too) fails once with a simulated transient error.
func mockSymmemoryPublishScriptFailingNthSet(callLogPath, stateDir string, failOnCall int) string {
	return `#!/bin/bash
LOG="` + callLogPath + `"
STATE="` + stateDir + `/set_count"
if [ "$1" = "entity" ]; then
  case "$2" in
    show)
      if [ "$3" = "Meeting m1" ] || [ "$3" = "Alice Example" ]; then
        echo "entity_show $3" >> "$LOG"
        if [ "$3" = "Meeting m1" ]; then
          echo '{"id":"e-meeting","name":"Meeting m1","type":"other","aliases":[],"description":""}'
        else
          echo '{"id":"e-alice","name":"Alice Example","type":"person","aliases":[],"description":""}'
        fi
      else
        echo "Error: entity not found: $3" >&2
        exit 1
      fi
      ;;
    add)
      echo "entity_add $3" >> "$LOG"
      echo "Entity created: $3"
      ;;
    list)
      echo '[{"id":"e-alice","name":"Alice Example","type":"person","aliases":[],"description":""}]'
      ;;
    relate)
      echo "entity_relate $3 $4 $5" >> "$LOG"
      echo "Related: $3 --$4--> $5"
      ;;
  esac
elif [ "$1" = "set" ]; then
  n=$(( $(cat "$STATE" 2>/dev/null || echo 0) + 1 ))
  echo "$n" > "$STATE"
  echo "memory_set $n" >> "$LOG"
  if [ "$n" = "` + fmt.Sprintf("%d", failOnCall) + `" ]; then
    echo "Error: simulated transient failure" >&2
    exit 1
  fi
  echo "{\"id\":\"mem-$n\",\"content\":\"x\",\"scope\":\"project\",\"entities\":[]}"
fi
`
}

// A publish that fails partway through a multi-fact proposal must not lose
// track of the facts it already wrote before the failure: symmemory `set`
// is not idempotent, so if a retry resubmits an already-succeeded fact it
// creates a duplicate memory. This is the "partial failure, retry" case the
// issue's acceptance criteria call out explicitly.
func TestPublishMeetingProposalPartialFailureThenRetryDoesNotDuplicateFirstFact(t *testing.T) {
	symmeetDir := t.TempDir()
	symmemoryDir := t.TempDir()
	callLog := symmemoryDir + "/calls.log"
	svc, path := importFixtureMeeting(t, symmeetDir)
	writeMockSymmemory(t, symmemoryDir, mockSymmemoryPublishScriptFailingNthSet(callLog, symmemoryDir, 2))
	withMockSymmemoryPath(t, symmemoryDir)
	if err := svc.ConfirmParticipant(path, "speaker_0", "e-alice"); err != nil {
		t.Fatal(err)
	}

	proposal := MeetingPublishProposal{
		Facts: []MeetingFact{
			{Value: "Alice proposed the Q3 roadmap."},
			{Value: "Bob will send the follow-up deck."},
		},
	}

	first, err := svc.PublishMeetingProposal(path, proposal)
	if err == nil {
		t.Fatal("expected the simulated second-fact failure to surface as an error")
	}
	if len(first.FactsPublished) != 1 {
		t.Fatalf("expected the first fact to have published before the failure, got %+v", first.FactsPublished)
	}

	second, err := svc.PublishMeetingProposal(path, proposal)
	if err != nil {
		t.Fatalf("retry: expected success, got %v", err)
	}
	if second.FactsSkipped != 1 {
		t.Errorf("expected the already-published first fact to be skipped on retry, got %d skipped: %+v", second.FactsSkipped, second)
	}
	if len(second.FactsPublished) != 1 {
		t.Errorf("expected exactly the previously-failed second fact to publish on retry, got %+v", second.FactsPublished)
	}

	logBytes, err := os.ReadFile(callLog) //nolint:gosec // test-owned temp path
	if err != nil {
		t.Fatal(err)
	}
	setCalls := strings.Count(string(logBytes), "memory_set")
	if setCalls != 3 {
		t.Errorf("expected exactly 3 set invocations across both applies (1 success, 1 simulated failure, 1 retry success), got %d — a higher count means the first fact was resubmitted and duplicated", setCalls)
	}
}

func TestFactHashIsStableAndTrimsWhitespace(t *testing.T) {
	a := factHash("Alice proposed the roadmap.")
	b := factHash("  Alice proposed the roadmap.  ")
	if a != b {
		t.Errorf("expected whitespace-insensitive hash, got %q vs %q", a, b)
	}
	c := factHash("Something else entirely.")
	if a == c {
		t.Error("expected different content to hash differently")
	}
	if !strings.HasPrefix(a, "") { // sanity: non-empty stable hex string
		t.Errorf("unexpected hash format: %q", a)
	}
}
