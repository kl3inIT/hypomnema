-- name: InsertMeeting :exec
-- The words to hear right are the shared list's since issue #747; the meeting's own terms are left empty.
INSERT INTO meetings (id, title, titled, language, people, file_name, media_type, size, state, created_at, updated_at)
VALUES (@id, @title, @titled, @language, @people, @file_name, @media_type, @size, 'transcribing', @now, @now);

-- name: InsertMeetingNotes :exec
-- The notes are sectioned by the meeting's topics since issue #771; the format column keeps 'auto'.
INSERT INTO meeting_notes (meeting_id, format, state, updated_at) VALUES (@meeting_id, 'auto', 'waiting', @now);

-- name: GetMeeting :one
SELECT * FROM meetings WHERE id = @id;

-- name: GetMeetingNotes :one
SELECT * FROM meeting_notes WHERE meeting_id = @meeting_id;

-- name: ListMeetings :many
-- The newest first; with a query, those whose title or a line holds it.
SELECT m.*, n.state AS notes_state, n.summary AS notes_summary,
       (SELECT count(*) FROM meeting_items i WHERE i.meeting_id = m.id AND i.kind = 'action') AS actions,
       (SELECT count(*) FROM meeting_items i WHERE i.meeting_id = m.id AND i.kind = 'action' AND i.done = 0)
           AS open_actions
FROM meetings m
JOIN meeting_notes n ON n.meeting_id = m.id
WHERE CAST(@query AS TEXT) = ''
   OR m.title LIKE '%' || @query || '%'
   OR EXISTS (SELECT 1 FROM meeting_lines l WHERE l.meeting_id = m.id AND l.text LIKE '%' || @query || '%')
ORDER BY m.created_at DESC, m.id DESC
LIMIT @lim;

-- name: ListMeetingSpeakers :many
SELECT * FROM meeting_speakers WHERE meeting_id = @meeting_id ORDER BY position, label;

-- name: ListSpeakersOfMeetings :many
-- The voices of every meeting listed, for their cards.
SELECT s.meeting_id, s.label, s.position, s.name,
       CAST(coalesce((SELECT sum(l.end_ms - l.start_ms) FROM meeting_lines l
                      WHERE l.meeting_id = s.meeting_id AND l.speaker = s.label), 0) AS INTEGER) AS talk_ms
FROM meeting_speakers s
WHERE s.meeting_id IN (sqlc.slice('ids'))
ORDER BY s.meeting_id, s.position, s.label;

-- name: ListMeetingLines :many
SELECT * FROM meeting_lines WHERE meeting_id = @meeting_id ORDER BY n;

-- name: GetMeetingLine :one
SELECT * FROM meeting_lines WHERE meeting_id = @meeting_id AND n = @n;

-- name: ListMeetingItems :many
SELECT * FROM meeting_items WHERE meeting_id = @meeting_id ORDER BY kind, position;

-- name: GetMeetingItem :one
SELECT * FROM meeting_items WHERE id = @id;

-- name: ListMeetingActions :many
-- Every action of every meeting, the newest meeting first, with its meeting's title and the name of its voice.
SELECT i.*, m.title AS meeting_title, m.created_at AS meeting_created_at, s.name AS speaker_name,
       s.position AS speaker_position
FROM meeting_items i
JOIN meetings m ON m.id = i.meeting_id
LEFT JOIN meeting_speakers s ON s.meeting_id = i.meeting_id AND s.label = i.owner_speaker
WHERE i.kind = 'action'
ORDER BY m.created_at DESC, m.id, i.position;

-- name: ClaimMeetingTranscription :execrows
-- A transcription is taken up: counted, so that one the control plane keeps stopping in ends.
UPDATE meetings SET attempts = attempts + 1, updated_at = @now WHERE id = @id AND state = 'transcribing';

-- name: MeetingsTranscribing :many
SELECT id FROM meetings WHERE state = 'transcribing' ORDER BY created_at, id;

-- name: NotesWriting :many
SELECT n.meeting_id FROM meeting_notes n JOIN meetings m ON m.id = n.meeting_id
WHERE n.state = 'writing' AND m.state = 'transcribed'
ORDER BY n.updated_at, n.meeting_id;

-- name: FailMeeting :exec
UPDATE meetings SET state = 'failed', failure = @failure, updated_at = @now WHERE id = @id;

-- name: TranscribedMeeting :exec
UPDATE meetings SET state = 'transcribed', failure = '', duration_ms = @duration_ms, provider = @provider,
    model = @model, language = CASE WHEN language = '' THEN @language ELSE language END, updated_at = @now
WHERE id = @id;

-- name: InsertMeetingSpeaker :exec
INSERT INTO meeting_speakers (meeting_id, label, position, name) VALUES (@meeting_id, @label, @position, @name);

-- name: InsertMeetingLine :exec
INSERT INTO meeting_lines (meeting_id, n, start_ms, end_ms, speaker, heard, text)
VALUES (@meeting_id, @n, @start_ms, @end_ms, @speaker, @heard, @text);

-- name: DeleteMeetingTranscript :exec
DELETE FROM meeting_lines WHERE meeting_id = @meeting_id;

-- name: DeleteMeetingSpeakers :exec
DELETE FROM meeting_speakers WHERE meeting_id = @meeting_id;

-- name: SetNotesState :exec
UPDATE meeting_notes SET state = @state, failure = @failure, updated_at = @now WHERE meeting_id = @meeting_id;

-- name: WriteNotesAgain :execrows
-- The owner asks for the notes again; not while they are being written or the transcript is not there.
UPDATE meeting_notes SET state = 'writing', failure = '', attempts = 0, updated_at = @now
WHERE meeting_id = @meeting_id AND state IN ('ready', 'failed');

-- name: ClaimNotes :execrows
UPDATE meeting_notes SET attempts = attempts + 1, updated_at = @now WHERE meeting_id = @meeting_id AND state = 'writing';

-- name: SaveNotes :exec
UPDATE meeting_notes SET state = 'ready', failure = '', model = @model, summary = @summary, sections = @sections,
    written_at = @now, updated_at = @now
WHERE meeting_id = @meeting_id;

-- name: SetWrittenTitle :exec
-- The title the notes wrote, for a meeting the owner did not title.
UPDATE meetings SET title = @title, updated_at = @now WHERE id = @id AND titled = 0;

-- name: DeleteMeetingItems :exec
DELETE FROM meeting_items WHERE meeting_id = @meeting_id;

-- name: InsertMeetingItem :exec
INSERT INTO meeting_items (id, meeting_id, kind, position, text, owner, owner_speaker, due, quote, line, start_ms)
VALUES (@id, @meeting_id, @kind, @position, @text, @owner, @owner_speaker, @due, @quote, @line, @start_ms);

-- name: SetMeetingItemDone :execrows
UPDATE meeting_items SET done = @done WHERE id = @id AND kind = 'action';

-- name: NameMeetingSpeaker :exec
UPDATE meeting_speakers SET name = @name WHERE meeting_id = @meeting_id AND label = @label;

-- name: MoveMeetingLines :exec
-- Every line of one voice is shown as another's.
UPDATE meeting_lines SET speaker = @to_label WHERE meeting_id = @meeting_id AND speaker = @from_label;

-- name: MoveMeetingLine :exec
UPDATE meeting_lines SET speaker = @speaker WHERE meeting_id = @meeting_id AND n = @n;

-- name: MoveMeetingItemOwners :exec
UPDATE meeting_items SET owner_speaker = @to_label WHERE meeting_id = @meeting_id AND owner_speaker = @from_label;

-- name: DeleteSilentSpeakers :exec
-- Voices left with no line and owning no action, after a line or a voice was given to another.
DELETE FROM meeting_speakers
WHERE meeting_speakers.meeting_id = @meeting_id
  AND label NOT IN (SELECT l.speaker FROM meeting_lines l WHERE l.meeting_id = @meeting_id)
  AND label NOT IN (SELECT i.owner_speaker FROM meeting_items i
                    WHERE i.meeting_id = @meeting_id AND i.owner_speaker IS NOT NULL);

-- name: DeleteMeeting :execrows
DELETE FROM meetings WHERE id = @id;

-- name: GetMeetingSettings :one
SELECT * FROM meeting_settings WHERE id = 1;

-- name: SetMeetingSettings :exec
INSERT INTO meeting_settings (id, notes_model, updated_at) VALUES (1, @notes_model, @now)
ON CONFLICT (id) DO UPDATE SET notes_model = excluded.notes_model, updated_at = excluded.updated_at;

-- name: SetMeetingLineText :execrows
-- The owner's fix of what a line says (issue #747).
UPDATE meeting_lines SET text = @text WHERE meeting_id = @meeting_id AND n = @n;

-- name: ListMeetingWords :many
-- The owner's words to hear right, in the order they were added (issue #747).
SELECT word FROM meeting_words ORDER BY created_at, word;

-- name: InsertMeetingWord :exec
INSERT OR IGNORE INTO meeting_words (word, created_at) VALUES (@word, @now);

-- name: DeleteMeetingWord :exec
DELETE FROM meeting_words WHERE word = @word;

-- name: MeetingSpeakerNames :many
-- The names the owner gave voices, the most recently heard first: words to hear right in the next meetings, as
-- Nojoin's people library carries names across recordings.
SELECT CAST(s.name AS TEXT) AS name FROM meeting_speakers s JOIN meetings m ON m.id = s.meeting_id
WHERE s.name IS NOT NULL
GROUP BY s.name
ORDER BY max(m.created_at) DESC, s.name;

-- name: MeetingBotNames :many
-- The Bots' names, read as words to hear right; the bots table is the Bots' own.
SELECT name FROM bots ORDER BY position, created_at, name;

-- name: MeetingRepoNames :many
-- The registered repositories' names, read as words to hear right; the repos table is the Runs' own.
SELECT name FROM repos ORDER BY created_at, name;
