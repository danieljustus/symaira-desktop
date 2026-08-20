package ingest

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"
)

// StartWorker starts a background worker loop that polls and processes jobs from the store.
// It runs until the context is cancelled.
func StartWorker(ctx context.Context, p *Pipeline) {
	log.Println("Starting ingest background worker...")
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Println("Stopping ingest background worker...")
			return
		default:
			// Claim a job
			job, err := p.Store.ClaimJob(ctx)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Worker error claiming job: %v\n", err)
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
				}
				continue
			}

			if job == nil {
				// No jobs, wait for ticker
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
				}
				continue
			}

			fmt.Fprintf(os.Stderr, "[Worker] Processing job %d (file: %s, kind: %s, attempt: %d)\n",
				job.ID, job.SourcePath, job.Kind, job.Attempts)

			// Process the job
			res, err := p.processJob(ctx, job, nil)
			if err != nil {
				fmt.Fprintf(os.Stderr, "[Worker] Job %d failed: %v\n", job.ID, err)
				if failErr := p.Store.FailJob(ctx, job.ID, err.Error()); failErr != nil {
					fmt.Fprintf(os.Stderr, "[Worker] Failed to record failure for job %d: %v\n", job.ID, failErr)
				}
				moveFailedSource(job.SourcePath, p.FailedDir, "process", err)
				time.Sleep(10 * time.Millisecond)
				continue
			}

			// Save vault and archive paths and complete the job
			if err := p.Store.SetVaultAndArchivePath(ctx, job.DocumentID, res.VaultPath, res.ArchivePath, res.Category, res.Tags, res.Correspondent, res.DocumentType); err != nil {
				fmt.Fprintf(os.Stderr, "[Worker] Failed to set vault/archive paths for job %d: %v\n", job.ID, err)
				if failErr := p.Store.FailJob(ctx, job.ID, fmt.Sprintf("set vault/archive paths: %v", err)); failErr != nil {
					fmt.Fprintf(os.Stderr, "[Worker] Failed to record failure for job %d: %v\n", job.ID, failErr)
				}
				moveFailedSource(job.SourcePath, p.FailedDir, "metadata", err)
				time.Sleep(10 * time.Millisecond)
				continue
			}

			if err := p.Store.CompleteJob(ctx, job.ID); err != nil {
				fmt.Fprintf(os.Stderr, "[Worker] Failed to complete job %d: %v\n", job.ID, err)
				time.Sleep(10 * time.Millisecond)
				continue
			}

			moveProcessedSource(job.SourcePath, p.ProcessedDir)
			fmt.Fprintf(os.Stderr, "[Worker] Job %d completed successfully. Note written: %s\n", job.ID, res.VaultPath)
			time.Sleep(10 * time.Millisecond)
		}
	}
}

func moveProcessedSource(path, processedDir string) {
	if processedDir == "" || processedDir == "." {
		return
	}
	if _, err := moveFileToDir(path, processedDir); err != nil {
		fmt.Fprintf(os.Stderr, "[Worker] Failed to move processed file %s: %v\n", path, err)
	}
}

func moveFailedSource(path, failedDir, stage string, err error) {
	if failedDir == "" || failedDir == "." {
		return
	}
	failedPath, moveErr := moveFileToDir(path, failedDir)
	if moveErr != nil {
		fmt.Fprintf(os.Stderr, "[Worker] Failed to move failed file %s: %v\n", path, moveErr)
		return
	}
	if sidecarErr := writeFailureSidecar(failedPath, stage, err); sidecarErr != nil {
		fmt.Fprintf(os.Stderr, "[Worker] Failed to write error sidecar for %s: %v\n", failedPath, sidecarErr)
	}
}
