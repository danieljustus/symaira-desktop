import XCTest
@testable import SymroomKit

/// Hermetic tests: JSON decoding against the exact shapes the `symroom` CLI
/// emits (`member list --json`, `run list --pending --json`, `log --json`).
/// No binary is required — the decoder is exercised on fixture payloads.
final class SymroomModelsTests: XCTestCase {
    private var decoder: JSONDecoder {
        let d = JSONDecoder()
        d.keyDecodingStrategy = .convertFromSnakeCase
        return d
    }

    func testMemberListJSONDecoding() throws {
        let fixture = """
        [
          {"id": "mem_abc123", "name": "alice", "role": "owner", "kind": "human"},
          {"id": "mem_def456", "name": "bob", "role": "agent", "kind": "agent"}
        ]
        """
        let members = try decoder.decode([RoomMember].self, from: Data(fixture.utf8))
        XCTAssertEqual(members.count, 2)
        XCTAssertEqual(members[0].name, "alice")
        XCTAssertEqual(members[0].role, "owner")
        XCTAssertEqual(members[1].kind, "agent")
        XCTAssertEqual(members[1].id, "mem_def456")
    }

    func testRunListJSONDecoding() throws {
        let fixture = """
        [
          {
            "id": "run_1", "title": "Deploy", "state": "requested", "author": "alice",
            "created_at": "2026-08-01T10:00:00.000Z", "updated_at": "2026-08-01T10:00:00.000Z",
            "scope": "local", "adapter": "openai", "plan_file": "plan.md",
            "checkpoints": [{"id": "chk_1", "run_id": "run_1", "question": "Continue?",
                             "state": "requested", "author": "alice",
                             "created_at": "2026-08-01T10:00:00.000Z"}]
          },
          {
            "id": "run_2", "title": "Nightly sync", "state": "approved", "author": "bob",
            "created_at": "2026-08-01T11:00:00.000Z", "updated_at": "2026-08-01T11:05:00.000Z",
            "scope": "repo", "expires_at": "2026-08-01T12:00:00.000Z", "approval_id": "ev_9"
          }
        ]
        """
        let runs = try decoder.decode([Run].self, from: Data(fixture.utf8))
        XCTAssertEqual(runs.count, 2)
        XCTAssertTrue(runs[0].isPending)
        XCTAssertFalse(runs[1].isPending)
        XCTAssertEqual(runs[0].scope, "local")
        XCTAssertEqual(runs[1].expiresAt, "2026-08-01T12:00:00.000Z")
        XCTAssertEqual(runs[0].checkpoints?.first?.question, "Continue?")
        XCTAssertEqual(runs[0].planFile, "plan.md")
    }

    func testJournalNDJSONDecoding() throws {
        // `symroom log --json` emits NDJSON; the client splits on newlines.
        let fixture = """
        {"v":1,"id":"ev_1","room":"test","author":"alice","seq":1,"prev":"","lamport":1,"ts":"2026-08-01T10:00:00.000Z","kind":"note.posted","body":{"message":"hello"}}
        {"v":1,"id":"ev_2","room":"test","author":"bob","seq":2,"prev":"ev_1","lamport":2,"ts":"2026-08-01T10:01:00.000Z","kind":"run.requested","body":{"title":"Deploy"}}
        """
        let events = fixture.split(separator: "\n").compactMap { line in
            try? decoder.decode(JournalEvent.self, from: Data(line.utf8))
        }
        XCTAssertEqual(events.count, 2)
        XCTAssertEqual(events[0].kind, "note.posted")
        XCTAssertEqual(events[0].author, "alice")
        XCTAssertEqual(events[1].kind, "run.requested")
        XCTAssertEqual(events[1].body?.displayString.contains("Deploy"), true)
        XCTAssertEqual(events[1].prev, "ev_1")
    }

    func testEmptyMemberListJSON() throws {
        let members = try decoder.decode([RoomMember].self, from: Data("[]".utf8))
        XCTAssertTrue(members.isEmpty)
    }

    func testJournalNDJSONStrictDecodingRejectsMalformedLine() {
        // A truncated/corrupted line must fail loudly, never vanish silently.
        let fixture = """
        {"v":1,"id":"ev_1","room":"test","author":"alice","seq":1,"prev":"","lamport":1,"ts":"2026-08-01T10:00:00.000Z","kind":"note.posted","body":{"message":"hello"}}
        {"this is not json
        """
        XCTAssertThrowsError(try RoomCLIClient.decodeJournalLines(Data(fixture.utf8)))
    }
}
