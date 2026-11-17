import SwiftUI
import SymairaTheme
import SymDeskCore

struct IngestQueueView: View {
    @EnvironmentObject var core: DeskCore
    @State private var jobs: [IngestJob] = []
    @State private var totalJobs = 0
    @State private var pageOffset = 0
    private let pageSize = 100
    @State private var isLoading = false
    @State private var errorMessage: String? = nil
    @State private var timer: Timer? = nil
    @StateObject private var retryTracker = AsyncActionTracker<String>()

    var body: some View {
        VStack(spacing: 0) {
            headerView
            Divider()

            if isLoading && jobs.isEmpty {
                SymairaLoadingState("Loading ingestion jobs…")
                    .frame(maxWidth: .infinity, maxHeight: .infinity)
            } else if let err = errorMessage {
                errorState(err)
            } else if jobs.isEmpty {
                emptyState
            } else {
                jobsList
            }
        }
        .task {
            await fetchJobs()
            startTimer()
        }
        .onDisappear {
            timer?.invalidate()
        }
    }

    private var headerView: some View {
        HStack {
            VStack(alignment: .leading, spacing: 4) {
                Text("Ingest Queue")
                    .symairaText(.title)
                Text("Monitor active and historical document ingestion tasks")
                    .symairaText(.secondary)
            }
            Spacer()
            Button(action: {
                Task { await fetchJobs(offset: pageOffset) }
            }) {
                Label("Refresh", symairaIcon: .refresh)
            }
            .symairaButtonStyle(.secondary)
        }
        .padding(.horizontal, 24)
        .padding(.vertical, 16)
    }

    private func errorState(_ msg: String) -> some View {
        VStack(spacing: 12) {
            SymairaNotice(title: "Connection Failed", message: msg, tone: .critical)
            Button("Retry Connection") {
                Task { await fetchJobs(offset: pageOffset) }
            }
            .symairaButtonStyle(.primary)
        }
        .frame(maxWidth: 520)
        .padding(.horizontal, 40)
        .frame(maxWidth: .infinity, maxHeight: .infinity)
    }

    private var emptyState: some View {
        SymairaEmptyState(
            icon: .inbox,
            title: "Queue Is Empty",
            message: "Drop PDF or image files to start ingestion."
        )
    }

    private var jobsList: some View {
        ScrollView {
            VStack(spacing: 10) {
                ForEach(jobs) { job in
                    JobRow(
                        job: job,
                        isRetrying: retryTracker.isInFlight(job.id),
                        onRetry: { Task { await retryJob(job) } }
                    )
                    .padding(14)
                    .symairaCard(padding: 0)
                    .asyncActionAlert(retryTracker, id: job.id, title: "Retry Failed") {
                        Task { await retryJob(job) }
                    }
                }
                paginationFooter
            }
            .padding(.horizontal, 24)
            .padding(.bottom, 16)
        }
    }

    private var paginationFooter: some View {
        HStack {
            Text("Jobs \(totalJobs == 0 ? 0 : pageOffset + 1)-\(min(pageOffset + jobs.count, totalJobs)) of \(totalJobs)")
                .symairaText(.secondary)
                .monospacedDigit()
            Spacer()
            Button("Previous") {
                Task { await fetchJobs(offset: max(0, pageOffset - pageSize)) }
            }
            .symairaButtonStyle(.secondary)
            .disabled(pageOffset == 0 || isLoading)
            Button("Next") {
                Task { await fetchJobs(offset: pageOffset + pageSize) }
            }
            .symairaButtonStyle(.secondary)
            .disabled(pageOffset + jobs.count >= totalJobs || isLoading)
        }
        .padding(.top, 6)
    }

    private func retryJob(_ job: IngestJob) async {
        let succeeded = await retryTracker.run(job.id) {
            try await core.ingestRetry(jobID: job.id)
        }
        guard succeeded else { return }
        await fetchJobs(offset: pageOffset)
    }

    private func fetchJobs(offset: Int = 0) async {
        errorMessage = nil
        isLoading = true
        do {
            let fetched = try await core.ingestJobPage(limit: pageSize, offset: offset)
            self.jobs = fetched.jobs.filter { job in
                let p = job.sourcePath.lowercased()
                return !p.contains("symdesk-watcher-test-") && !p.contains("/inbox_watch/test-document.txt")
            }
            self.totalJobs = fetched.total
            self.pageOffset = fetched.offset
        } catch {
            self.errorMessage = error.localizedDescription
        }
        isLoading = false
    }

    private func startTimer() {
        timer?.invalidate()
        timer = Timer.scheduledTimer(withTimeInterval: 4.0, repeats: true) { _ in
            Task {
                await fetchJobs()
            }
        }
    }
}

struct JobRow: View {
    let job: IngestJob
    let isRetrying: Bool
    let onRetry: () -> Void

    var body: some View {
        HStack(alignment: .top, spacing: 16) {
            statusIcon
                .padding(.top, 2)

            VStack(alignment: .leading, spacing: 4) {
                HStack {
                    Text(displayFilename)
                        .symairaText(.subheading)
                        .lineLimit(1)
                    Spacer()
                    Text("Job #\(job.id)")
                        .symairaText(.monoSmall)
                }
                Text(job.sourcePath)
                    .symairaText(.caption)
                    .lineLimit(1)
                    .truncationMode(.middle)

                HStack(spacing: 12) {
                    Label(job.kind.capitalized, symairaIcon: .document)
                    Label("Attempts: \(job.attempts)", symairaIcon: .restart)
                    if !job.createdAt.isEmpty {
                        Text("Created: \(formattedDate(job.createdAt))")
                    }
                }
                .symairaText(.secondary)

                if let err = job.lastError {
                    Text(err)
                        .symairaText(.caption, respectsForeground: false)
                        .foregroundStyle(SymairaTokens.critical)
                        .padding(8)
                        .background(SymairaTokens.criticalSoft)
                        .cornerRadius(6)
                        .padding(.top, 4)
                }
            }

            Spacer()

            if job.status == "failed" {
                Button(action: onRetry) {
                    if isRetrying {
                        ProgressView()
                            .controlSize(.small)
                    } else {
                        Text("Retry")
                    }
                }
                .symairaButtonStyle(.secondary)
                .disabled(isRetrying)
            }
        }
    }

    private var statusIcon: some View {
        switch job.status.lowercased() {
        case "completed", "success":
            return SymairaIconView(.success, size: 16)
                .foregroundStyle(SymairaTokens.positive)
        case "failed":
            return SymairaIconView(.failure, size: 16)
                .foregroundStyle(SymairaTokens.critical)
        case "running":
            return SymairaIconView(.sync, size: 16)
                .foregroundStyle(SymairaTokens.ink)
        default:
            return SymairaIconView(.time, size: 16)
                .foregroundStyle(SymairaTokens.ink2)
        }
    }

    private var displayFilename: String {
        let name = URL(fileURLWithPath: job.sourcePath).lastPathComponent
        return name.isEmpty ? job.sourcePath : name
    }

    private func formattedDate(_ rawStr: String) -> String {
        // rawStr is e.g. RFC3339 or ISO8601
        let f = ISO8601DateFormatter()
        f.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        if let d = f.date(from: rawStr) {
            let df = DateFormatter()
            df.dateStyle = .short
            df.timeStyle = .short
            return df.string(from: d)
        }
        return rawStr
    }
}
