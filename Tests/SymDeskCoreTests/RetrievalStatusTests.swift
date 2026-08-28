import XCTest
@testable import SymDeskCore

final class RetrievalStatusTests: XCTestCase {
    func testStoredDegradationDecodesIndependentlyOfBackendAvailability() throws {
        let json = """
        {
            "document_count": 3,
            "chunk_count": 9,
            "database_bytes": 4096,
            "last_indexed_at": "2026-08-28T10:00:00Z",
            "embedding_model": "qwen3-embedding:0.6b",
            "backend_available": true,
            "pending_chunk_count": 2,
            "mixed_embedding_spaces": false,
            "index_scope": "shared",
            "vault_document_count": 3
        }
        """.data(using: .utf8)!

        let status = try JSONDecoder().decode(RetrievalStatus.self, from: json)
        XCTAssertTrue(status.backendAvailable)
        XCTAssertTrue(status.hasStoredDegradation)
        XCTAssertEqual(status.pendingChunkCount, 2)
        XCTAssertEqual(status.indexScope, "shared")
        XCTAssertEqual(status.vaultDocumentCount, 3)
    }

    func testTemporaryBackendOutageDoesNotImplyStoredDegradation() throws {
        let json = """
        {
            "document_count": 3,
            "chunk_count": 9,
            "database_bytes": 4096,
            "embedding_model": "local-hash",
            "backend_available": false,
            "pending_chunk_count": 0,
            "mixed_embedding_spaces": false
        }
        """.data(using: .utf8)!

        let status = try JSONDecoder().decode(RetrievalStatus.self, from: json)
        XCTAssertFalse(status.backendAvailable)
        XCTAssertFalse(status.hasStoredDegradation)
    }

    func testMeetingListResultDecodesPerFileFailures() throws {
        let json = Data(#"{
            "meetings": [],
            "failures": [{"path":"meetings/broken.md","message":"invalid frontmatter"}]
        }"#.utf8)

        let result = try JSONDecoder().decode(MeetingListResult.self, from: json)
        XCTAssertTrue(result.meetings.isEmpty)
        XCTAssertEqual(result.failures, [MeetingListFailure(path: "meetings/broken.md", message: "invalid frontmatter")])
    }

    func testOlderStatusPayloadRemainsDecodable() throws {
        let json = """
        {
            "document_count": 1,
            "chunk_count": 2,
            "database_bytes": 1024,
            "embedding_model": "qwen3-embedding:0.6b",
            "backend_available": true
        }
        """.data(using: .utf8)!

        let status = try JSONDecoder().decode(RetrievalStatus.self, from: json)
        XCTAssertNil(status.pendingChunkCount)
        XCTAssertNil(status.mixedEmbeddingSpaces)
        XCTAssertFalse(status.hasStoredDegradation)
    }
}
