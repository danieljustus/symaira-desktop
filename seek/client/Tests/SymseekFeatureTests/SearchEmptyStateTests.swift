import XCTest
@testable import SymseekFeature

/// Issue #309: an errored search must never also claim a zero-result
/// outcome. The "no documents matched" empty state is reserved for
/// genuine successful empty results (search ran, no error, zero matches).
final class SearchEmptyStateTests: XCTestCase {

    private func makeResult() -> SearchView.SearchResult {
        let chunk = SearchView.Chunk(
            id: 1,
            uuid: "test-uuid",
            document_path: "/tmp/example.md",
            chunk_index: 0,
            content: "hello",
            hash: "abc"
        )
        return SearchView.SearchResult(
            chunk: chunk,
            bm25_rank: 1,
            vector_rank: 1,
            rrf_score: 0.5,
            cosine_score: 0.5
        )
    }

    // A successful search that returned zero matches shows the empty state.
    func testEmptyStateShownForSuccessfulEmptyResults() {
        XCTAssertTrue(shouldShowEmptyMessage(query: "foo", results: [], searchError: nil))
    }

    // An errored search must suppress the empty state even with zero results
    // — never "no documents matched" next to a connection error.
    func testEmptyStateSuppressedWhenErrorIsSet() {
        XCTAssertFalse(shouldShowEmptyMessage(query: "foo", results: [], searchError: "Connection refused"))
        XCTAssertFalse(shouldShowEmptyMessage(query: "foo", results: [], searchError: ""))
    }

    // With matches present there is no empty state, error or not.
    func testEmptyStateNotShownWhenResultsExist() {
        let results = [makeResult()]
        XCTAssertFalse(shouldShowEmptyMessage(query: "foo", results: results, searchError: nil))
        XCTAssertFalse(shouldShowEmptyMessage(query: "foo", results: results, searchError: "boom"))
    }

    // Without a query the empty state is not shown.
    func testEmptyStateNotShownWithoutQuery() {
        XCTAssertFalse(shouldShowEmptyMessage(query: "", results: [], searchError: nil))
    }
}
