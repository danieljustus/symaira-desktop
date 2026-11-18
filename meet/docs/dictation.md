# Push-to-talk dictation (Meet agent)

Dictation is an optional mode of the `SymMeetAgent` menu-bar app (issue #1401). It is **off by
default** and is a human input surface: it is controlled only from the agent's settings and the
keyboard. No MCP tool, CLI command or agent profile can start dictation or insert text;
`SymMeetMCP` does not reference any dictation code.

## Behavior

- Hold the hotkey (Fn / Globe by default, Right Option as an alternative) to talk, release to
  insert. A press shorter than 0.25 s is a tap and records nothing. Two taps within 0.35 s switch
  to hands-free; the next key press ends it. Esc cancels, also while text is being transcribed.
- The microphone is opened only while recording. Audio stays in memory; there is no history, no
  audio file and no transcript log.
- Speech recognition runs locally. Default engine: Parakeet Ultra through FluidAudio (pinned to
  0.18.3, model downloaded on first enable). WhisperKit remains selectable as a fallback.
- Optional vocabulary: `<vault>/.symdesk/dictation/{dictionary,corrections}.md`, from the
  agent setting or `SYMDESK_VAULT`. Missing files are not an error. `styles.md` is not read yet
  because `SymDictationCore` has no parser for it.

## Insertion rules (`SymDictationCore.InsertionPolicy`)

| Situation | Result |
|---|---|
| Secure Input active (password field) | Text stays in a card. Nothing is pasted, typed or put on the clipboard. |
| No Accessibility permission | Text stays in a card with a hint. |
| Focused control cannot take text | Text stays in a card. |
| Window or app changed while transcribing | Text stays in a card. |
| Chromium family, Electron app, or "always type" | Typed as unicode key events; the clipboard is untouched. |
| Anything else | Clipboard snapshot, Cmd+V, restore only if the pasteboard `changeCount` is unchanged and no newer session started. |

## Permissions

- Microphone: system prompt on first enable.
- Accessibility: needed for the key event tap (it consumes Esc while dictation is active) and for
  posting Cmd+V or typed characters. The agent is not sandboxed; no extra entitlement is needed.
  Without the permission the settings show a German hint and dictation waits; text is never lost.

## Code map

- `Sources/SymDictationCore`: gesture machine, `HotkeyEventMapper`, `InsertionPolicy`,
  `DictationInserter` (clipboard snapshot and restore), `DictationController`, vault loader.
  Platform neutral and fully unit tested with injected fakes.
- `Sources/SymMeetCore/Engines/SingleShotTranscription.swift`: in-memory recording through any
  `TranscriptionEngine`.
- `Sources/SymMeetParakeet`: `ParakeetEngine` behind `TranscriptionEngine`.
- `Sources/SymMeetAgent/Dictation`: CGEventTap, AVAudioEngine recorder, pasteboard and CGEvent
  adapters, Accessibility probing, non-activating panels, settings and onboarding.
