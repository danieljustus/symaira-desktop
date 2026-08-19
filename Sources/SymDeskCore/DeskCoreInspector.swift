import Foundation

extension DeskCore {
    public func docProps(path: String) async throws -> [String: String] {
		let data = try await runChecked(arguments: ["props", "get", path, "--json"] + vaultArgs)
        return try DocumentProperties.decode(data)
    }

    public func docNoteContent(path: String) async throws -> String {
		guard let transport else { throw DeskCoreError.coreNotFound }
		return try await transport.fileContent(path: path)
    }

	public func saveNoteContent(path: String, content: String) async throws {
		guard let transport else { throw DeskCoreError.coreNotFound }
		try await transport.saveFile(path: path, content: content)
	}

	public func remoteCachedFile(path: String) async throws -> URL {
		guard let remoteClient else { throw ServerConnectionError.missingConfiguration }
		return try await remoteClient.cachedFile(path: path)
	}

    public func docSetType(path: String, type: String) async throws {
        try await noteEditProperty(path: path, key: "document_type", value: type)
    }

    public func docSetTitle(path: String, title: String) async throws {
        try await noteEditProperty(path: path, key: "title", value: title)
    }

    public func docSetCorrespondent(path: String, name: String) async throws {
        try await noteEditProperty(path: path, key: "correspondent", value: name)
    }

    public func docSetDocumentDate(path: String, date: String) async throws {
        try await noteEditProperty(path: path, key: "document_date", value: date)
    }

    public func docSetPerson(path: String, person: String) async throws {
        try await noteEditProperty(path: path, key: "person", value: person)
    }

    public func docSetNoteVisible(path: String, visible: Bool) async throws {
        try await noteEditProperty(path: path, key: "note_visible", value: visible ? "true" : "false")
    }

    /// Marks (or unmarks) a file as "not a document" for Review Lane purposes.
    /// Persisted as frontmatter, so the dismissal survives a later index
    /// refresh instead of the entry resurfacing (issue #228).
    public func docSetReviewIgnored(path: String, ignored: Bool) async throws {
        try await noteEditProperty(path: path, key: "review_ignored", value: ignored ? "true" : "false")
    }

    public func docSetTags(path: String, tags: String) async throws {
        try await noteEditProperty(path: path, key: "tags", value: tags)
    }
}
