# Known Limitations

## Current limitations

This is the first stable release. The following limitations are known and will be
addressed in future releases.

### No cloud transcription

All transcription runs locally on the device. There is no cloud fallback
or remote processing option.

### No automatic meeting detection

The tool does not detect or start recording meetings automatically.
Users must explicitly start each recording session.

### Apple Silicon only

The release supports Apple Silicon (arm64) on macOS 15 or newer. Intel
Mac support is planned for a future release.

### No live captions

Real-time captioning during recording is not yet supported. Transcripts
are available after the recording stops and processing completes.

### Limited diarization

Speaker diarization is basic. The tool identifies different speakers but
does not map them to known individuals without manual review.

### No encryption at rest

Meeting artifacts are stored as plain files on disk. Users are
responsible for disk-level encryption if their data requires it.

### Model size

The recommended multilingual model requires ~626 MB of disk space.
The tiny model (~75 MB) is available for constrained environments but
produces lower-quality transcripts.

## Workarounds

| Limitation | Workaround |
|-----------|-----------|
| No cloud transcription | Use a local model; ensure sufficient disk space |
| No auto-detection | Use `symmeet record` with explicit start/stop |
| Intel Mac not supported | Use an Apple Silicon Mac or wait for future support |
| No live captions | Review the transcript after recording |
| Basic diarization | Use the review UI to correct speaker labels |
| No encryption at rest | Enable FileVault or use encrypted disk images |
| Large model size | Use the `tiny` model for testing |
