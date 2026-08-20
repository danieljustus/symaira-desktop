# Getting Started

## Install

```bash
# Via Homebrew (when available)
brew install danieljustus/tap/symmeet

# Or build from source
git clone https://github.com/danieljustus/symaira-meet.git
cd symaira-meet
make build
```

## First steps

```bash
# Check installation
symmeet version --json

# Check system status
symmeet doctor --json

# List existing meetings
symmeet meeting list --json
```

## Install a model

```bash
# List available models
symmeet model list --json

# Install the tiny model (~75 MB)
symmeet model install tiny
```

## Transcribe a file

```bash
symmeet transcribe recording.m4a --model tiny --json
```

## Export a transcript

```bash
# Export as Markdown
symmeet export <meeting-id> --format markdown --output transcript.md

# Export as subtitles
symmeet export <meeting-id> --format srt --output transcript.srt
```

## Record a meeting

```bash
symmeet record --purpose "Team standup"
```

## MCP server

```bash
# Start the MCP stdio server for agent integration
symmeet mcp
```
