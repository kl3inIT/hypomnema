# Soniox on v6, chosen models, captions from TTS timestamps, speed and retries

Issue #1285, approved by the owner on 2026-11-27. What the Soniox integration did before, what Soniox's documents and
the references do, and what was built.

Sources read on 2026-11-27: docs.soniox.com `/stt/models`, `/tts/rt/timestamps`, `/api-reference/tts/websocket-api`,
`/api-reference/tts/generate_tts`, `/api-reference/tts/get_tts_models`, `/api-reference/tts/get_shared_voices`,
`/tts/concepts/voices`, `/tts/concepts/voice-cloning`, `/tts/rest-api/limits-and-quotas`, `/api-reference/errors`.
Clones in `.tmp/`: pipecat `fed424b`, livekit-agents `57b6906` (only `livekit-plugins-soniox`), soniox-python
`248315e`, elevenlabs-python `a902350`.

## What Hypomnema did

- Models were constants: STT `stt-async-v5` (`speech/speech.go:70`), realtime `stt-rt-v5` (`speech/live.go:38`), TTS
  `tts-rt-v2` (`speech.go:71`). Soniox deprecated v5 when v6 shipped on 2026-11-03; v5 is removed on 2026-12-15, and
  calls to it are routed to v6 after that (`/stt/models`).
- TTS over REST only (`speech/soniox.go:262-266`) with `text, model, language, voice, audio_format`: no `speed`, no
  voice list (the one voice "Adrian", OmniRoute's default).
- No retry in `soniox.go` or `http.go`.
- Studio timed its captions by sending every spoken clip back to Soniox STT (`videos/voice.go:216-262`).

## What the documents and the references do

| | Models | TTS options | TTS timestamps | Voices | Errors |
|---|---|---|---|---|---|
| Soniox docs | STT `stt-async-v6`, `stt-rt-v6` (v5 removed 2026-12-15); TTS `tts-rt-v2` (`/stt/models`, `/tts/models`) | `speed` 0.7-1.3, `reduce_silence` (only where `supports_silence_reduction`), `sample_rate`, `bitrate`, `client_reference_id` (at most 256); at most 5000 characters; REST audio truncated at 2 min; 100 requests a minute, 3 at once (`generate_tts`, `limits-and-quotas`) | WebSocket only: `return_timestamps: true` gives `timestamps.characters`, `character_start_times_seconds`, `character_end_times_seconds` with each audio frame, over the preprocessed text, with no map back to the input (`/tts/rt/timestamps`) | `GET /v1/shared-voices?model=` (paged by `next_page_cursor`, limit 1-200), `GET /v1/voices` for cloned voices with a per-model `status`, `GET /v1/tts-models` with `supports_timestamps`, `supports_silence_reduction`, `speed_min/max` | retry 408, 429, 500, 503; 402 means the balance is spent (`/api-reference/errors`) |
| pipecat `services/soniox/` | STT default `stt-rt-v6` (`stt.py:122`), TTS configurable | `speed`, `reduce_silence`, `client_reference_id` only when set (`tts.py:131-151`, `_build_config_msg` `:449-470`) | always `return_timestamps`; characters cut into words at spaces, a `[tag]` one word, `*` skipped (`_to_word_times`, `tts.py:537-575`) | voice id | in-stream `error_code` classified by its HTTP status (`errors.py:14-29`) |
| LiveKit `livekit-plugins-soniox` | configurable; TTS default stale (`tts-rt-v1-preview`, `tts.py:45`) | `speed` checked 0.7-1.3 (`tts.py:53-54, 150`), `reduce_silence` refused for models listed without it (`:65-74`) | not used | voice id | framework retry options |
| soniox-python | `GET /models`, `GET /tts-models` (`api/models.py`, `api/tts_models.py`) | `speed` 0.7-1.3, `text` at most 5000 (`types/api.py:529-535`) | | `list_shared`, `list` with cursor pages (`api/async_voices.py:34-170`) | async job polled every 5 s (`api/async_stt.py:225-246`); 429 a rate-limit error (`errors.py:142`) |
| elevenlabs-python (Fern) | | | | | 2 retries (`core/http_client.py:379`) of 408, 409, 429 and 5xx (`:121-123`), after `Retry-After` (at most 60 s) else 1 s doubling with ±10 % jitter (`:99-118`) |

What they share: the model is a setting whose default is the newest; `speed` is checked against 0.7-1.3; captions
come from the TTS timestamps over the WebSocket (pipecat, the only reference current on TTS v2); errors are read by
their status and the retryable ones asked again.

## What was built

| # | Built | From |
|---|---|---|
| 1 | STT on `stt-async-v6`, realtime on `stt-rt-v6`. The STT and TTS model of each provider is a setting (`gateway_audio_providers.stt_model`, `tts_model`), empty for the newest (`speech.DefaultModel`). Settings → Audio lists Soniox's TTS models from `GET /v1/tts-models` and its async STT models from `GET /v1/models`, aliases left out. The realtime model stays a constant. | `/stt/models`; pipecat `stt.py:122` |
| 2 | Studio asks its speech with `Timestamps`; Soniox then speaks over `wss://tts-rt.soniox.com/tts-websocket` with `return_timestamps: true`, and the words, built by `speech.AlignedWords`, are kept beside the clip, so captioning makes no STT call. The STT pass stays for a clip without them. `audio_speak` and the gateway keep REST. | `/tts/rt/timestamps`; pipecat `tts.py:449-470, 537-575, 589-660` |
| 2a | `speech.AlignedWords` is shared: any provider whose answer has per-character times (ElevenLabs's `alignment` has the same three arrays, issue #1283) builds its words with it. The words are matched back to the line's own text by a longest common subsequence of their letters and digits (NFC, lower case); a run of the line's words between two matches (such as "59%" spoken as five words) shares the time of the spoken words between them. | cutting: pipecat `_to_word_times`; matching back: Hypomnema's own, since no reference maps the normalized text back and the issue's row 2 asks for the line's spelling |
| 3 | `speed` checked to 0.7-1.3 and sent to Soniox (a named Soniox refuses it with 400, otherwise Soniox is skipped with the reason); `reduce_silence` sent only to a model whose catalog entry supports it. | `generate_tts`; LiveKit `tts.py:65-74, 150`; pipecat `tts.py:131-151` |
| 3a | A Studio video has its own speed (the owner, 2026-11-27, answering that Studio had none): `video_revisions.speed`, 0.7-1.3 or 0 for the provider's own, chosen in the make dialog by tenths or `hypomnema videos make --speed`, kept by a look change, a retry and a revision of changed lines, and part of a clip's cache key when not 0. | pipecat and LiveKit take `speed` as an option of the voice; no reference has a video's speed |
| 4 | 408, 429, 500 and 503 asked again twice, after `Retry-After` or 1 s then 2 s with ±10 % jitter; a 402 reads "Soniox balance is empty"; `client_reference_id` is the request's reference, a new `hypomnema-<hex>` when none. A `Retry-After` over 30 s is not waited for: the key rests and the next key is asked (Hypomnema's own, since its keys are a pool where the SDK has one). | Soniox errors; elevenlabs-python `core/http_client.py:99-123, 379` |
| 5 | Voices: `GET /v1/shared-voices?model=<the TTS model>` (each page) and the cloned voices of `GET /v1/voices` ready for that model, kept a day a key, for `GET /gateway/audio/voices` (the voice picker, by name) and the gateway's `/v1/audio/voices`. | `/tts/concepts/voices`; soniox-python `api/async_voices.py` |
| 6 | Each key's Soniox speech waits for a place: at most 3 at once and 100 starts in any minute; a line over 5000 characters is refused before the call. | `/tts/rest-api/limits-and-quotas`; soniox-python `types/api.py:535` |
| 7 | The async job is polled every 5 s instead of 1 s; nothing else changed. | soniox-python `api/async_stt.py:225-246` |

Left out, as the issue says: the realtime endpoint knobs, echo cancellation and agent text input, cloned voices made
from Hypomnema.
