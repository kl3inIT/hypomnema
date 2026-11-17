# Meetings, meeting-note imports and the gateway's audio routes

Date: 2026-11-17. Asked by the owner: a meetings feature like MemoryOS's, later importing meeting notes from
Microsoft Teams and Lark, and speech-to-text (STT) and text-to-speech (TTS) in the model gateway first, with Soniox
and then ElevenLabs. Read from the clones in `.tmp/` and the official Microsoft Graph pages; nothing was called live.

## Meetings in the references

| Reference | How a meeting comes in | Speech to text | After the meeting | Imports provider notes |
|---|---|---|---|---|
| MemoryOS (owner) | Browser records mic + the meeting tab over a WebSocket; or a recording is uploaded (≤ 500 MB, deleted after) | Soniox, live and async, diarized per track | Summary, decisions, actions (owner, due, quote) from one model call, then checks; Word/PDF minutes; open in chat | Not built (MEM-169) |
| Hyprnote / Anarlog (MIT outside `enterprise/`) | Desktop mic + system audio; upload | ~40 adapters, local or cloud | Jinja templates, `action_items` table | Teams (Graph), Google Meet, Zoom: `crates/meeting-import/src/` |
| Meetily (MIT) | Desktop mic + system audio; upload | Local Whisper/Parakeet, no diarization | JSON templates, split-then-combine | None |
| Vexa (Apache-2.0) | Playwright bot joins Meet/Teams/Zoom | faster-whisper, near real time | A coding agent writes notes | Manual transcript import |
| Attendee (Elastic 2.0, not OSS) | SDK/Selenium bots | Many cloud providers | None | None |

Sources: MemoryOS `docs/specs/meeting.md`, `docs/increments/completed/meeting-notes/design.md`,
`memoryos-core/src/main/resources/db/migration/V108__meetings.sql`, `V109__meeting_minutes.sql`,
`memoryos-core/src/main/java/io/memoryos/ai/TranscriptSummarizer.java:89-123`; Hyprnote
`crates/db-app/migrations/20260710223922_canonical_data_model.sql`, `crates/meeting-import/src/teams.rs`;
Meetily `frontend/src-tauri/migrations/20250916100000_initial_schema.sql`; Vexa
`core/meetings/modules/join/src/`; Attendee `bots/models.py`.

Bots that join calls (Vexa, Attendee) need a browser per meeting, a bot account and per-platform page code; importing
finished notes through the platforms' APIs needs none of that.

## Importing meeting notes

- **Microsoft Teams (Graph, GA):** `GET /users/{id}/onlineMeetings/{meetingId}/transcripts` and `.../content`
  (`text/vtt` with speaker names); `getAllTranscripts(meetingOrganizerUserId=…)` with `delta` (application
  permission only); change notifications on `users/{id}/onlineMeetings/getAllTranscripts`. Permission
  `OnlineMeetingTranscript.Read.All`; application permission needs the admin's application access policy. Teams APIs
  are not metered since 2025-08-25. Copilot's `aiInsights` needs a Microsoft 365 Copilot licence per user.
  https://learn.microsoft.com/en-us/graph/api/onlinemeeting-getalltranscripts?view=graph-rest-1.0 ,
  https://learn.microsoft.com/en-us/graph/teams-changenotifications-callrecording-and-calltranscript ,
  https://learn.microsoft.com/en-us/graph/metered-api-list
- **Lark:** Minutes `GET /open-apis/minutes/v1/minutes/:minute_token`, `/transcript` (txt or srt, with speakers),
  `/media`, and `/artifacts` (AI summary, todos, chapters and transcript in one JSON); VC `GET /vc/v1/notes/{note_id}`
  (AI notes and verbatim docs); events `minutes.minute.generated_v1`, `vc.note.generated_v1`,
  `vc.meeting.all_meeting_ended_v1`. Scopes `minutes:minutes.basic:read`, `minutes:minutes.artifacts:read`,
  `minutes:minutes.media:export`, `vc:note:read`, `vc:record:readonly`. From the Lark Go SDK
  (`oapi-sdk-go/v3@v3.7.2/service/minutes/v1`, `service/vc/v1`) and lark-cli (`shortcuts/minutes/minutes_detail.go`,
  `shortcuts/vc/vc_notes.go:201-252, 460-487`, `events/minutes/register.go`); Lark's own doc pages could not be
  fetched. No reference imports Lark minutes. Hypomnema's Lark user connection (`integration/larkuser.go`) can carry
  the scopes.

## Audio in the model gateways

| Gateway | Soniox | Speaker in the answer | Long files | Usage | Key scope |
|---|---|---|---|---|---|
| OmniRoute | STT (async) and TTS | `segments[].speaker`, `words[].speaker` in `verbose_json` | Blocking, polls ≤ 120 s | seconds; characters for TTS | endpoint category `audio` |
| LiteLLM | STT async and realtime | `Speaker N:` in the text; `speaker` on realtime segments | Blocking, polls ≈ 30 min | seconds | route `audio_transcriptions` |
| Bifrost | — | `diarized_json` segments with `speaker` (OpenAI's shape) | Blocking, or async job routes | seconds | — |
| 9router / 9router-go | — | ElevenLabs `words[].speaker_id` passed through | Blocking; 128 MB body (go) | — | per model (go) |
| new-api | — | none | Blocking | minutes as tokens | not checked |

Sources: OmniRoute `open-sse/handlers/audioTranscription.ts:126, 161-187, 484, 507-627`,
`open-sse/config/audioRegistry.ts:203, 465`, `src/shared/constants/endpointCategories.ts:52`,
`src/lib/usage/mediaRequestAccounting.ts:89-91`; LiteLLM `litellm/llms/soniox/audio_transcription/handler.py`,
`common_utils.py:24-46, 88`, `llms/soniox/realtime/transformation.py:49, 226-240`; Bifrost
`core/schemas/transcriptions.go:23, 141-147, 241-246`, `core/providers/elevenlabs/transcription.go:221`,
`transports/bifrost-http/handlers/asyncinference.go:79-93`; 9router-go `internal/middleware/max_body.go:18-23`,
`internal/handlers/media/elevenlabs_stt.go:173-178, 385-400`; MemoryOS `voice/SonioxApi.java:24-42`,
`voice/SonioxAsync.java:28, 77, 85-151`, `voice/SonioxLiveTranscription.java:120-134`.

All serve OpenAI's `POST /v1/audio/transcriptions` (multipart `file` + `model`) and `POST /v1/audio/speech`; a
job-based provider (Soniox, AssemblyAI) is hidden behind one blocking request that polls. ElevenLabs is the second
provider most of them carry for both STT with speakers and TTS (OmniRoute, 9router, 9router-go, Bifrost).

## Decided (owner, 2026-11-17)

- The gateway gets STT and TTS first, with several providers in the owner's order: Soniox, then ElevenLabs (issue
  to follow). Meetings come next, cut down for one user, then live recording, then Lark and Teams imports. No bot
  joins calls.
