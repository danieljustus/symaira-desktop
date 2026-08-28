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
            "mixed_embedding_spaces": false
        }
        """.data(using: .utf8)!

        let status = try JSONDecoder().decode(RetrievalStatus.self, from: json)
        XCTAssertTrue(status.backendAvailable)
        XCTAssertTrue(status.hasStoredDegradation)
        XCTAssertEqual(status.pendingChunkCount, 2)
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
