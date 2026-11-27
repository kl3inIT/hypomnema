# How mature projects integrate ElevenLabs text to speech

Date: 2026-11-27. Issue #1283: ElevenLabs speech was made with `eleven_multilingual_v2`, hard-coded
(`internal/controlplane/speech/speech.go:73` before the change), with no `language_code`, so Vietnamese lines came
out wrong; Studio timed captions by transcribing each clip again with Soniox (`videos/voice.go:220-245`). Clones are
in `.tmp/` (git-ignored). Read for the change: pipecat `fed424b`, elevenlabs-python `a902350`, agent-demo-video
(OrionArchitekton) `de61695`. The LiveKit, openclaw, open-webui and aidemo rows are from the issue's own reading of
those clones; the lines are theirs.

## What was measured (2026-11-27, the issue)

"Ủa, bánh mì giảm năm mươi chín phần trăm kìa? Rẻ hơn cốc trà sữa luôn!" spoken, then transcribed with Soniox:

| Model | Voice | Heard back |
|---|---|---|
| `eleven_multilingual_v2` (Hypomnema `audio_speak`) | Adam, Sơn Trần, Ninh Đôn, Hảo | garbled ("Wah, bạn mới dám nói…") |
| `eleven_v3`, `language_code: vi` | Sơn Trần | exact |
| `eleven_v4`, `language_code: vi` | Adam, Sơn Trần | exact, and `[laughs]` is heard as a laugh |
| `/with-timestamps` with `eleven_v4`, `eleven_v4_turbo` | Sơn Trần | 200, with `alignment` and `normalized_alignment` that keep Vietnamese diacritics; the audio tag `[excited]` appears in the characters |
| `/with-timestamps` with `eleven_flash_v2_5` | Sơn Trần | 200, but `normalized_alignment` drops diacritics ("giam", "phan") |

## Comparison

| | Default model | Model configurable | `language_code` | voice_settings | Caption / word timing | Voices |
|---|---|---|---|---|---|---|
| LiveKit (`livekit-plugins-elevenlabs/.../tts.py`) | `eleven_turbo_v2_5` (`:113`) | yes, per instance (`:296-315`) | sent when set (`:1433`) | exposed (`:73-79`) | WebSocket alignment to timed words (`:1525-1555`) | id, `GET /v1/voices` (`:1417`) |
| pipecat (`services/elevenlabs/tts.py`) | `eleven_flash_v2_5` (`:385`) | yes | sent, but dropped with a warning when the model does not cover the language (`tts_base.py:194-225`; v4 lists `vi`) | only the keys that are set (`:103-114`) | `/stream/with-timestamps`, `alignment` split into words (`:1095`, `tts_base.py:371-400`) | id, required |
| openclaw (`extensions/elevenlabs`) | `eleven_multilingual_v2` (`speech-provider-factory.ts:32`) | config, per call (`:289-298`) | sent when set, ISO 639-1 checked (`src/tts/tts-provider-helpers.ts:28-37`) | defaults 0.5/0.75/0/true/1 (`:33-39`) | none | id, `/v1/voices` (`:421-465`) |
| open-webui (`routers/audio/__init__.py`) | admin setting from `GET /v1/models` (`:1588-1601`) | yes | no | fixed (`:622`) | none | id checked against `/v1/voices` |
| agent-demo-video (`src/tts.ts`, `captions.ts`) | `eleven_multilingual_v2` (`types.ts:88`) | config | no | 0.5/0.75 plus seed (`types.ts:90-91`) | `/with-timestamps` per shot (`tts.ts:104-131`); `alignmentWords` gives word times for captions (`captions.ts:46-65`) | id |
| aidemo (`src/voice.ts`, `captions.ts`) | `eleven_multilingual_v2` (`config.ts:239`) | env var | no | speed only | Whisper transcribes the audio again (`captions.ts:162`), as Hypomnema did with Soniox | id |
| official SDK | server decides (`model_id` omitted) | per call | optional | all optional | `convert_with_timestamps` returns `alignment`/`normalized_alignment` | id |

What they share:
- The model is configurable, never hard-coded.
- `language_code` is sent only when the caller sets it.
- voice_settings are optional.
- The voice is an id, and voices are listed from `GET /v1/voices`.
- Retries come from the client: the SDK makes 2 with backoff and honours `Retry-After`, at most 60 s, else 1 s
  doubling with jitter, for 429, 408, 409 and 5xx (`core/http_client.py:21-22, 99-123, 379-382`).

Details read for the build:
- pipecat's guard (`tts_base.py:184-225`) keeps a static table of the languages each model takes and sends no
  `language_code` for a model outside it (multilingual v2: ElevenLabs documents the parameter as unsupported
  there). Hypomnema takes the languages from `GET /v1/models` (`languages[].language_id`, SDK
  `types/model.py:83`), as the issue decided, so a new model needs no code.
- pipecat prefers `alignment` unless a pronunciation dictionary is set (`_select_alignment`, `tts_base.py:371-400`);
  agent-demo-video splits the characters on whitespace, a word starting at its first character and ending at its
  last (`captions.ts:46-65`).

## Decided (owner, 2026-11-27)

The issue's Design table, rows 1-7: the model a setting picked from `GET /v1/models`, `eleven_v4` by default;
`language_code` when the caller gives a language and the model covers it; Studio's voicing on `/with-timestamps`
with caption words from `alignment`, audio tags dropped; voice_settings as before; voices from `GET /v1/voices`;
two retries honouring `Retry-After`; output `mp3_44100_128`. Left out: searching the shared voice library, the
WebSocket and streaming APIs, pronunciation dictionaries. Price: `elevenlabs/eleven_v4` is $0.08 per 1,000
characters (https://elevenlabs.io/pricing/api, read 2026-11-27), already in the LiteLLM snapshot of `units.json`.

Choices made while building, which no reference settles:
- When `GET /v1/models` cannot be read, or does not list the model, the language is sent and ElevenLabs judges it,
  rather than dropped on a guess. A model older than v2.5 (`eleven_multilingual_v2`, `_v1`, `turbo_v2`,
  `flash_v2`), which pipecat's table leaves out so that none is sent (`tts_base.py:176-191`), never gets one; a
  language is sent as its ISO 639-1 code (`en-US` is `en`) (review of #1300).
- A pinned video revision (a look change, a retry) also pins the model its voice was spoken with (`pin_model`), so a
  later change of the owner's model does not re-speak it; the pin's provider and voice were already pinned (#908).
  A pin made before `pin_model` takes the model the revision was spoken with, before the default.
- The retry covers ElevenLabs's speech and its two lists, not a Scribe transcription (a 128 MiB upload); the SDK's
  jitter is left out, as one control plane asks. Each attempt has its own 2-minute budget and the waits are apart
  from it, as the SDK's timeout is one request's (`http_client.py:379-382`), so two `Retry-After: 60` still leave
  the third attempt (review of #1300).
- Studio keeps the alignment's words as they are, each with its own times: no mark said on its own is glued to the
  word before (review of #1300). Soniox's voice for videos stays typed in Settings → Audio; only ElevenLabs's is
  picked from its list (row 5; Soniox's is #1285).
