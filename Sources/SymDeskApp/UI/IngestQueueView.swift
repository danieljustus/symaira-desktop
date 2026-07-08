import SwiftUI
import SymDeskCore

struct IngestQueueView: View {
    @EnvironmentObject var core: DeskCore
    @State private var jobs: [IngestJob] = []
    @State private var isLoading = false
    @State private var errorMessage: String? = nil
    @State private var timer: Timer? = nil

    var body: some View {
        VStack(spacing: 0) {
            headerView
            Divider()

            if isLoading && jobs.isEmpty {
                ProgressView("Loading ingestion jobs…")
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
                    .font(.title2)
                    .fontWeight(.bold)
                Text("Monitor active and historical document ingestion tasks")
                    .font(.subheadline)
                    .foregroundColor(.secondary)
            }
            Spacer()
            Button(action: {
                Task { await fetchJobs() }
            }) {
                Label("Refresh", systemImage: "arrow.clockwise")
            }
            .buttonStyle(.bordered)
        }
        .padding(.horizontal, 24)
        .padding(.vertical, 16)
        .background(Color(nsColor: .windowBackgroundColor))
    }

    private func errorState(_ msg: String) -> some View {
        VStack(spacing: 12) {
            Image(systemName: "exclamationmark.triangle")
                .font(.system(size: 48))
                .foregroundColor(.red)
            Text("Connection Failed")
                .font(.title3)
                .fontWeight(.semibold)
            Text(msg)
                .font(.body)
                .foregroundColor(.secondary)
                .multilineTextAlignment(.center)
                .padding(.horizontal, 40)
            Button("Retry Connection") {
                Task { await fetchJobs() }
            }
            .buttonStyle(.borderedProminent)
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
    }

    private var emptyState: some View {
        VStack(spacing: 12) {
            Image(systemName: "tray")
                .font(.system(size: 48))
                .foregroundColor(.secondary)
            Text("Queue is empty")
                .font(.title3)
                .foregroundColor(.secondary)
            Text("Drop PDF or image files to start ingestion.")
                .font(.caption)
                .foregroundColor(.secondary)
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
    }

    private var jobsList: some View {
        List {
            ForEach(jobs) { job in
                JobRow(job: job) {
                    Task {
                        try? await core.ingestRetry(jobID: job.id)
                        await fetchJobs()
                    }
                }
                .padding(.vertical, 8)
                Divider()
            }
        }
        .listStyle(.plain)
        .padding(.horizontal, 16)
    }

    private func fetchJobs() async {
        errorMessage = nil
        isLoading = true
        do {
            self.jobs = try await core.ingestJobs()
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
    let onRetry: () -> Void

    var body: some View {
        HStack(alignment: .top, spacing: 16) {
            statusIcon
                .font(.title2)
                .padding(.top, 2)

            VStack(alignment: .leading, spacing: 6) {
                HStack {
                    Text(job.sourcePath)
                        .font(.headline)
                        .lineLimit(1)
                    Spacer()
                    Text("Job #\(job.id)")
                        .font(.system(.caption, design: .monospaced))
                        .foregroundColor(.secondary)
                }

                HStack(spacing: 12) {
                    Label(job.kind.capitalized, systemImage: "doc.text")
                    Label("Attempts: \(job.attempts)", systemImage: "arrow.counterclockwise")
                    Text("Created: \(formattedDate(job.createdAt))")
                }
                .font(.caption)
                .foregroundColor(.secondary)

                if let err = job.lastError {
                    Text(err)
                        .font(.caption)
                        .foregroundColor(.red)
                        .padding(8)
                        .background(Color.red.opacity(0.1))
                        .cornerRadius(6)
                        .padding(.top, 4)
                }
            }

            Spacer()

            if job.status == "failed" {
                Button(action: onRetry) {
                    Text("Retry")
                }
                .buttonStyle(.borderedProminent)
                .controlSize(.small)
                .tint(.blue)
            }
        }
    }

    private var statusIcon: some View {
        switch job.status.lowercased() {
        case "completed", "success":
            return Image(systemName: "checkmark.circle.fill")
                .foregroundColor(.green)
        case "failed":
            return Image(systemName: "xmark.circle.fill")
                .foregroundColor(.red)
        case "running":
            return Image(systemName: "arrow.triangle.2.circlepath.circle.fill")
                .foregroundColor(.blue)
        default:
            return Image(systemName: "clock.fill")
                .foregroundColor(.orange)
        }
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
