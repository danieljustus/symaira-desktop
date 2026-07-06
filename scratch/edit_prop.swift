    public func noteEditProperty(path: String, key: String, value: String) async throws {
        guard let tool else { throw DeskCoreError.coreNotFound }
        let runner = CLIRunner()
        // symdesk props edit <path> <key> <value>
        _ = try await runner.runChecked(
            tool.location.url,
            arguments: ["props", "edit", path, key, value]
        )
    }
