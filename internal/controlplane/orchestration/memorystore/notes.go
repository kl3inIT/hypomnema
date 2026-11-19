package memorystore

// A memory as a directory (issue #589, docs/research/2026-11-16-memory-as-a-directory.md and
// 2026-11-18-long-term-memory.md). An agent or a Bot sees its memory as /memories and works with it through the
// six commands of Anthropic's memory tool (memory_20250818: view, create, str_replace, insert, delete, rename), whose
// result strings are kept so a model trained on that tool reads them as it expects. Anthropic leaves the storage to
// the application:
//
//   - /memories/MEMORY.md is the memory kept before notes, in the prompt every time: an agent's (the memories
//     table) or a Bot's (bots.memory), 2,200 characters, one entry per line (Hermes's MEMORY.md);
//   - /memories/USER.md is the memory about the owner, the same in every directory (Hermes's and OpenClaw's
//     USER.md);
//   - every other file is a note (memory_notes), read only when needed, at most MaxNotes of MaxNoteBytes each
//     (Coder's MaxMemories and MaxMemoryBodyBytes, coderd/x/chatd/chattool/memory.go), listed with its size at the
//     start so the agent knows what it may read (OpenMausBot's topic index, server/workspace.ts).
//
// Each write reads the directory again inside its transaction, so writers at once do not undo each other (as
// Apply); a write over a limit is refused and changes nothing (Hermes, Coder).

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/storeutil"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/storeerr"
	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
)

// The directory and its two files that are not notes.
const (
	Root      = "/memories"
	IndexFile = "MEMORY.md"
	OwnerFile = "USER.md"
)

// Limits of the notes: Coder's (MaxMemories, MaxMemoryBodyBytes), and the view's cut, Anthropic's ("truncates the
// text view of files longer than 16,000 characters").
const (
	MaxNotes     = 200
	MaxNoteBytes = 8192
	maxViewChars = 16000
	// maxDepth is how many parts a path has below /memories: Anthropic's view lists two levels.
	maxDepth = 2
)

// ofBot is a Bot's notes in memory_notes, named by its id.
const ofBot = "bot"

// Commands of the memory tool.
const (
	View       = "view"
	Create     = "create"
	StrReplace = "str_replace"
	Insert     = "insert"
	Delete     = "delete"
	Rename     = "rename"
)

// Commands is every command, in the order the tool lists them.
var Commands = []string{View, Create, StrReplace, Insert, Delete, Rename}

// Command is one call of the memory tool, in Anthropic's parameters.
type Command struct {
	Command    string  `json:"command"`
	Path       string  `json:"path,omitempty"`
	ViewRange  []int   `json:"view_range,omitempty"`
	FileText   *string `json:"file_text,omitempty"`
	OldStr     *string `json:"old_str,omitempty"`
	NewStr     *string `json:"new_str,omitempty"`
	InsertLine *int    `json:"insert_line,omitempty"`
	InsertText *string `json:"insert_text,omitempty"`
	OldPath    string  `json:"old_path,omitempty"`
	NewPath    string  `json:"new_path,omitempty"`
}

// Dir is whose memory directory: the owner's, an agent's or a Bot's.
type Dir struct {
	kind string
	name string
}

// AgentDir is the directory of an agent, by its Profile's name.
func AgentDir(name string) Dir { return Dir{kind: ofAgent, name: name} }

// BotDir is the directory of a Bot, by its id.
func BotDir(id string) Dir { return Dir{kind: ofBot, name: id} }

// OwnerDir contains only the shared USER.md, with no agent's index or notes.
func OwnerDir() Dir { return Dir{kind: ofOwner} }

// Note is a note's path under /memories, its size, and when it was written.
type Note struct {
	Path      string
	Bytes     int
	UpdatedAt time.Time
}

// NoteText is a note with its text.
type NoteText struct {
	Note

	Text string
}

// CommandError is a command the agent can fix, its result as Anthropic's tool words it. It is ErrInvalid.
type CommandError string

func (e CommandError) Error() string { return string(e) }

// Is makes a CommandError an ErrInvalid.
func (e CommandError) Is(target error) bool { return target == storeerr.ErrInvalid }

func refusef(format string, args ...any) error { return CommandError(fmt.Sprintf(format, args...)) }

var partRE = regexp.MustCompile(`^[A-Za-z0-9_-][A-Za-z0-9._-]{0,63}$`)

// relPath is a path as the agent writes it, relative to /memories: "" for /memories itself. It refuses what could
// leave the directory, as Anthropic asks: a path outside /memories, `..` and `.` parts, a `%` that could encode
// them, a backslash, a hidden part, and more than two levels.
func relPath(p string) (string, error) {
	p = strings.TrimSpace(p)

	switch {
	case p == Root || p == Root+"/":
		return "", nil
	case !strings.HasPrefix(p, Root+"/"):
		return "", refusef("Error: the path %s is not under %s. Every path starts with %s/.", p, Root, Root)
	case strings.ContainsAny(p, `%\`):
		return "", refusef("Error: the path %s has a %% or a backslash; write it plainly.", p)
	}

	rel := strings.TrimSuffix(strings.TrimPrefix(p, Root+"/"), "/")
	parts := strings.Split(rel, "/")

	if len(parts) > maxDepth {
		return "", refusef("Error: the path %s is more than %d levels below %s.", p, maxDepth, Root)
	}

	for _, part := range parts {
		if !partRE.MatchString(part) {
			return "", refusef("Error: %q in %s is not a name: use letters, digits, '.', '_' and '-', not starting "+
				"with '.', at most 64 characters.", part, p)
		}
	}

	return rel, nil
}

func full(rel string) string {
	if rel == "" {
		return Root
	}

	return Root + "/" + rel
}

// fixed says whether a path is one of the two files that are not notes.
func fixed(rel string) bool { return rel == IndexFile || rel == OwnerFile }

// dirState is a directory as one command sees it, read inside its transaction.
type dirState struct {
	index, owner string
	notes        []store.ListMemoryNotesRow
	ownerOnly    bool
}

func (d dirState) note(rel string) bool {
	return slices.ContainsFunc(d.notes, func(n store.ListMemoryNotesRow) bool { return n.Path == rel })
}

func (d dirState) folder(rel string) bool {
	return slices.ContainsFunc(d.notes, func(n store.ListMemoryNotesRow) bool {
		return strings.HasPrefix(n.Path, rel+"/")
	})
}

func (d dirState) exists(rel string) bool {
	return rel == "" || fixed(rel) || d.note(rel) || d.folder(rel)
}

func (s *Store) readDir(ctx context.Context, q *store.Queries, dir Dir) (dirState, error) {
	var (
		d   dirState
		err error
	)

	owner, err := readMemory(ctx, q, ofOwner, "")
	if err != nil {
		return dirState{}, err
	}

	d.owner = owner.Text
	d.ownerOnly = dir.kind == ofOwner

	if d.ownerOnly {
		return d, nil
	}

	if d.index, err = readIndex(ctx, q, dir); err != nil {
		return dirState{}, err
	}

	if d.notes, err = q.ListMemoryNotes(ctx, store.ListMemoryNotesParams{Kind: dir.kind, Name: dir.name}); err != nil {
		return dirState{}, fmt.Errorf("listing the notes of %s: %w", dirNoun(dir), err)
	}

	return d, nil
}

// readIndex is the directory's MEMORY.md: an agent's memory or a Bot's.
func readIndex(ctx context.Context, q *store.Queries, dir Dir) (string, error) {
	if dir.kind == ofBot {
		b, err := q.GetBot(ctx, dir.name)
		if err != nil {
			return "", storeutil.NotFound(err, "bot", dir.name)
		}

		return b.Memory, nil
	}

	m, err := readMemoryIn(ctx, q, ofAgent, dir.name)

	return m.Text, err
}

func writeIndex(ctx context.Context, q *store.Queries, dir Dir, text string, now int64) error {
	if dir.kind == ofBot {
		return q.SetBotMemory(ctx, store.SetBotMemoryParams{ID: dir.name, Memory: text, Now: now})
	}

	return q.SetMemory(ctx, store.SetMemoryParams{Kind: ofAgent, Name: dir.name, Memory: text, Now: now})
}

func dirNoun(dir Dir) string {
	if dir.kind == ofBot {
		return "the memory of bot " + dir.name
	}

	return "the memory of agent " + dir.name
}

// DispatchDir is the directory of the agent a Dispatch runs as.
func (s *Store) DispatchDir(ctx context.Context, dispatchID string) (Dir, error) {
	agent, err := dispatchAgent(ctx, s.read(), dispatchID)
	if err != nil {
		return Dir{}, err
	}

	return AgentDir(agent), nil
}

// Notes lists a directory's notes, for the start of a chat or a task and for the owner.
func (s *Store) Notes(ctx context.Context, dir Dir) ([]Note, error) {
	q := s.read()

	if _, err := readIndex(ctx, q, dir); err != nil {
		return nil, err
	}

	rows, err := q.ListMemoryNotes(ctx, store.ListMemoryNotesParams{Kind: dir.kind, Name: dir.name})
	if err != nil {
		return nil, fmt.Errorf("listing the notes of %s: %w", dirNoun(dir), err)
	}

	out := make([]Note, 0, len(rows))
	for _, r := range rows {
		out = append(out, Note{Path: full(r.Path), Bytes: int(r.Bytes.Int64), UpdatedAt: time.UnixMilli(r.UpdatedAt).UTC()})
	}

	return out, nil
}

// AgentNotesTx lists an agent's notes with the caller's queries, for a worker's offer.
func AgentNotesTx(ctx context.Context, q *store.Queries, agent string) ([]string, error) {
	rows, err := q.ListMemoryNotes(ctx, store.ListMemoryNotesParams{Kind: ofAgent, Name: agent})
	if err != nil {
		return nil, fmt.Errorf("listing the notes of agent %s: %w", agent, err)
	}

	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, agenttool.MemoryNote(full(r.Path), int(r.Bytes.Int64)))
	}

	return out, nil
}

// ReadNote returns one note, for the owner.
func (s *Store) ReadNote(ctx context.Context, dir Dir, path string) (NoteText, error) {
	rel, err := notePath(path)
	if err != nil {
		return NoteText{}, err
	}

	row, err := s.read().GetMemoryNote(ctx, store.GetMemoryNoteParams{Kind: dir.kind, Name: dir.name, Path: rel})
	if err != nil {
		return NoteText{}, storeutil.NotFound(err, "note", full(rel))
	}

	return NoteText{
		Note: Note{Path: full(rel), Bytes: len(row.Text), UpdatedAt: time.UnixMilli(row.UpdatedAt).UTC()},
		Text: row.Text,
	}, nil
}

// WriteNote creates or replaces one note with the owner's text, within the limits.
func (s *Store) WriteNote(ctx context.Context, dir Dir, path, text string) (NoteText, error) {
	rel, err := notePath(path)
	if err != nil {
		return NoteText{}, err
	}

	if err := s.write(ctx, func(q *store.Queries, now int64) error {
		d, err := s.readDir(ctx, q, dir)
		if err != nil {
			return err
		}

		if !d.note(rel) {
			if err := d.room(rel); err != nil {
				return err
			}
		}

		return setNote(ctx, q, dir, rel, text, now)
	}); err != nil {
		return NoteText{}, fmt.Errorf("writing %s of %s: %w", full(rel), dirNoun(dir), err)
	}

	return s.ReadNote(ctx, dir, full(rel))
}

// DeleteNote removes one note, for the owner.
func (s *Store) DeleteNote(ctx context.Context, dir Dir, path string) error {
	rel, err := notePath(path)
	if err != nil {
		return err
	}

	return s.write(ctx, func(q *store.Queries, _ int64) error {
		if _, err := q.GetMemoryNote(ctx, store.GetMemoryNoteParams{Kind: dir.kind, Name: dir.name, Path: rel}); err != nil {
			return storeutil.NotFound(err, "note", full(rel))
		}

		return q.DeleteMemoryNote(ctx, store.DeleteMemoryNoteParams{Kind: dir.kind, Name: dir.name, Path: rel})
	})
}

// DeleteBotNotes removes every note of a Bot, inside the caller's transaction, when it is removed with its memory.
func DeleteBotNotes(ctx context.Context, q *store.Queries, id string) error {
	return q.DeleteMemoryNotesOf(ctx, store.DeleteMemoryNotesOfParams{Kind: ofBot, Name: id})
}

// BotHasNotes says whether a Bot keeps notes, inside the caller's transaction.
func BotHasNotes(ctx context.Context, q *store.Queries, id string) (bool, error) {
	n, err := q.CountMemoryNotes(ctx, store.CountMemoryNotesParams{Kind: ofBot, Name: id})

	return n > 0, err
}

// notePath is a path the owner names a note by: under /memories, and neither MEMORY.md nor USER.md, which he
// edits as the memory they are.
func notePath(path string) (string, error) {
	rel, err := relPath(path)

	switch {
	case err != nil:
		return "", fmt.Errorf("%w: %s", storeerr.ErrInvalid, err.Error())
	case rel == "" || fixed(rel):
		return "", fmt.Errorf("%w: %s is not a note", storeerr.ErrInvalid, full(rel))
	}

	return rel, nil
}

// room says whether a new note fits at rel: its parent is not a note, and the directory has room for one more.
func (d dirState) room(rel string) error {
	if parent, _, ok := strings.Cut(rel, "/"); ok && (fixed(parent) || d.note(parent)) {
		return refusef("Error: %s is a file, not a directory", full(parent))
	}

	if d.folder(rel) {
		return refusef("Error: %s is a directory", full(rel))
	}

	if len(d.notes) >= MaxNotes {
		return refusef("Error: the memory holds %d notes, its limit; merge or delete stale notes first", MaxNotes)
	}

	return nil
}

func setNote(ctx context.Context, q *store.Queries, dir Dir, rel, text string, now int64) error {
	if n := len(text); n > MaxNoteBytes {
		return refusef("Error: %s would be %s bytes, over a note's limit of %s; split it or keep less",
			full(rel), thousands(n), thousands(MaxNoteBytes))
	}

	return q.SetMemoryNote(ctx, store.SetMemoryNoteParams{Kind: dir.kind, Name: dir.name, Path: rel, Text: text,
		Now: now})
}

// Run runs one command of the memory tool in a directory and returns its result. What the agent can fix is a
// CommandError; a change's result ends with how full the memory is (Usage).
func (s *Store) Run(ctx context.Context, dir Dir, c Command) (string, error) {
	if dir.kind == ofOwner {
		rel, err := relPath(c.Path)
		if err != nil {
			return "", err
		}

		switch {
		case c.Command == View && (rel == "" || rel == OwnerFile):
		case (c.Command == Insert || c.Command == StrReplace) && rel == OwnerFile:
		default:
			return "", refusef("Use view, insert or str_replace on %s", full(OwnerFile))
		}
	}

	if c.Command == View {
		q := s.read()

		d, err := s.readDir(ctx, q, dir)
		if err != nil {
			return "", err
		}

		return d.view(ctx, q, dir, c)
	}

	if !slices.Contains(Commands, c.Command) {
		return "", refusef("Error: unknown command %q; the commands are %s", c.Command, strings.Join(Commands, ", "))
	}

	var result string

	if err := s.write(ctx, func(q *store.Queries, now int64) error {
		d, err := s.readDir(ctx, q, dir)
		if err != nil {
			return err
		}

		ch := change{q: q, dir: dir, d: d, now: now}

		if result, err = ch.run(ctx, c); err != nil {
			return err
		}

		after, err := s.readDir(ctx, q, dir)
		if err != nil {
			return err
		}

		result += "\n" + after.usage(ch.owner)

		return nil
	}); err != nil {
		return "", err
	}

	return result, nil
}

// change is one write command inside its transaction.
type change struct {
	q   *store.Queries
	dir Dir
	d   dirState
	now int64
	// owner is set when the command wrote USER.md, whose use the result then gives.
	owner bool
}

func (ch *change) run(ctx context.Context, c Command) (string, error) {
	switch c.Command {
	case Create:
		return ch.create(ctx, c)
	case StrReplace:
		return ch.strReplace(ctx, c)
	case Insert:
		return ch.insert(ctx, c)
	case Delete:
		return ch.delete(ctx, c)
	default:
		return ch.rename(ctx, c)
	}
}

func (ch *change) create(ctx context.Context, c Command) (string, error) {
	rel, err := relPath(c.Path)
	if err != nil {
		return "", err
	}

	if c.FileText == nil {
		return "", refusef("Error: create needs file_text")
	}

	if ch.d.exists(rel) {
		return "", refusef("Error: File %s already exists", full(rel))
	}

	if err := ch.d.room(rel); err != nil {
		return "", err
	}

	if err := setNote(ctx, ch.q, ch.dir, rel, *c.FileText, ch.now); err != nil {
		return "", err
	}

	return "File created successfully at: " + full(rel), nil
}

// text is a file's text for an edit: MEMORY.md, USER.md or a note.
func (ch *change) text(ctx context.Context, rel string) (string, bool) {
	switch {
	case rel == IndexFile:
		return ch.d.index, true
	case rel == OwnerFile:
		return ch.d.owner, true
	case ch.d.note(rel):
		row, err := ch.q.GetMemoryNote(ctx, store.GetMemoryNoteParams{Kind: ch.dir.kind, Name: ch.dir.name, Path: rel})
		if err != nil {
			return "", false
		}

		return row.Text, true
	default:
		return "", false
	}
}

// save writes a file's text after an edit. MEMORY.md and USER.md keep their rule: an entry a line, empty and
// repeated lines dropped, within the limit (FromOwner).
func (ch *change) save(ctx context.Context, rel, text string) error {
	if !fixed(rel) {
		return setNote(ctx, ch.q, ch.dir, rel, text, ch.now)
	}

	limit := MaxMemory
	if rel == OwnerFile {
		limit = MaxOwnerMemory
	}

	memory, err := FromOwner(text, limit)
	if err != nil {
		return refusef("Error: %s would be %s characters, over its limit of %s; nothing was changed. Move detail "+
			"into a note, merge entries that say the same thing or remove stale ones first", full(rel),
			thousands(utf8.RuneCountInString(text)), thousands(limit))
	}

	if rel == OwnerFile {
		ch.owner = true

		return ch.q.SetMemory(ctx, store.SetMemoryParams{Kind: ofOwner, Name: "", Memory: memory, Now: ch.now})
	}

	return writeIndex(ctx, ch.q, ch.dir, memory, ch.now)
}

func (ch *change) editable(ctx context.Context, path string) (string, string, error) {
	rel, err := relPath(path)
	if err != nil {
		return "", "", err
	}

	text, ok := ch.text(ctx, rel)
	if !ok {
		return "", "", refusef("Error: The path %s does not exist. Please provide a valid path.", full(rel))
	}

	return rel, text, nil
}

func (ch *change) strReplace(ctx context.Context, c Command) (string, error) {
	rel, text, err := ch.editable(ctx, c.Path)
	if err != nil {
		return "", err
	}

	if c.OldStr == nil || *c.OldStr == "" {
		return "", refusef("Error: str_replace needs old_str")
	}

	old := *c.OldStr

	switch n := strings.Count(text, old); {
	case n == 0:
		return "", refusef("No replacement was performed, old_str `%s` did not appear verbatim in %s.", old, full(rel))
	case n > 1:
		return "", refusef("No replacement was performed. Multiple occurrences of old_str `%s` in lines: %s. "+
			"Please ensure it is unique", old, linesOf(text, old))
	}

	if err := ch.save(ctx, rel, strings.Replace(text, old, deref(c.NewStr), 1)); err != nil {
		return "", err
	}

	return "The memory file has been edited.", nil
}

// linesOf is the lines, from 1, on which old starts in text.
func linesOf(text, old string) string {
	var lines []string

	for i, at := 0, 0; ; {
		j := strings.Index(text[at:], old)
		if j < 0 {
			break
		}

		i += strings.Count(text[at:at+j], "\n")
		lines = append(lines, strconv.Itoa(i+1))
		i += strings.Count(old, "\n")
		at += j + len(old)
	}

	return strings.Join(lines, ", ")
}

func (ch *change) insert(ctx context.Context, c Command) (string, error) {
	rel, text, err := ch.editable(ctx, c.Path)
	if err != nil {
		return "", err
	}

	if c.InsertLine == nil || c.InsertText == nil {
		return "", refusef("Error: insert needs insert_line and insert_text")
	}

	lines := splitLines(text)

	at := *c.InsertLine
	if at < 0 || at > len(lines) {
		return "", refusef("Error: Invalid `insert_line` parameter: %d. It should be within the range of lines of "+
			"the file: [0, %d]", at, len(lines))
	}

	added := splitLines(*c.InsertText)
	lines = slices.Insert(lines, at, added...)

	if err := ch.save(ctx, rel, strings.Join(lines, "\n")); err != nil {
		return "", err
	}

	return fmt.Sprintf("The file %s has been edited.", full(rel)), nil
}

func splitLines(text string) []string {
	if text == "" {
		return nil
	}

	return strings.Split(strings.TrimSuffix(text, "\n"), "\n")
}

func (ch *change) delete(ctx context.Context, c Command) (string, error) {
	rel, err := relPath(c.Path)
	if err != nil {
		return "", err
	}

	switch {
	case rel == "":
		return "", refusef("Error: %s itself cannot be deleted", Root)
	case fixed(rel):
		return "", refusef("Error: %s cannot be deleted; remove its entries with str_replace instead", full(rel))
	case !ch.d.exists(rel):
		return "", refusef("Error: The path %s does not exist", full(rel))
	}

	for _, n := range ch.d.notes {
		if n.Path == rel || strings.HasPrefix(n.Path, rel+"/") {
			if err := ch.q.DeleteMemoryNote(ctx, store.DeleteMemoryNoteParams{
				Kind: ch.dir.kind, Name: ch.dir.name, Path: n.Path,
			}); err != nil {
				return "", err
			}
		}
	}

	return "Successfully deleted " + full(rel), nil
}

func (ch *change) rename(ctx context.Context, c Command) (string, error) {
	from, err := relPath(c.OldPath)
	if err != nil {
		return "", err
	}

	to, err := relPath(c.NewPath)
	if err != nil {
		return "", err
	}

	switch {
	case from == "" || to == "" || fixed(from) || fixed(to):
		return "", refusef("Error: %s, %s and %s cannot be renamed, nor be renamed to", Root, full(IndexFile),
			full(OwnerFile))
	case !ch.d.exists(from):
		return "", refusef("Error: The path %s does not exist", full(from))
	case ch.d.exists(to):
		return "", refusef("Error: The destination %s already exists", full(to))
	case strings.HasPrefix(to, from+"/"):
		return "", refusef("Error: %s cannot move into itself", full(from))
	}

	if parent, _, ok := strings.Cut(to, "/"); ok && ch.d.note(parent) {
		return "", refusef("Error: %s is a file, not a directory", full(parent))
	}

	for _, n := range ch.d.notes {
		if n.Path != from && !strings.HasPrefix(n.Path, from+"/") {
			continue
		}

		moved := to + strings.TrimPrefix(n.Path, from)
		if _, err := relPath(full(moved)); err != nil {
			return "", err
		}

		text, _ := ch.text(ctx, n.Path)
		if err := ch.q.DeleteMemoryNote(ctx, store.DeleteMemoryNoteParams{
			Kind: ch.dir.kind, Name: ch.dir.name, Path: n.Path,
		}); err != nil {
			return "", err
		}

		if err := setNote(ctx, ch.q, ch.dir, moved, text, ch.now); err != nil {
			return "", err
		}
	}

	return fmt.Sprintf("Successfully renamed %s to %s", full(from), full(to)), nil
}

// view is a directory's listing or a file's lines, Anthropic's way.
func (d dirState) view(ctx context.Context, q *store.Queries, dir Dir, c Command) (string, error) {
	rel, err := relPath(c.Path)
	if err != nil {
		return "", err
	}

	switch {
	case rel == "" || d.folder(rel):
		return d.listing(rel), nil
	case fixed(rel):
		text := d.index
		if rel == OwnerFile {
			text = d.owner
		}

		return viewFile(rel, text, c.ViewRange)
	case d.note(rel):
		text, err := readNoteIn(ctx, q, dir, rel)
		if err != nil {
			return "", err
		}

		return viewFile(rel, text, c.ViewRange)
	default:
		return "", refusef("The path %s does not exist. Please provide a valid path.", full(rel))
	}
}

func (d dirState) listing(rel string) string {
	type line struct {
		path string
		size int
	}

	var files []line

	if rel == "" {
		if !d.ownerOnly {
			files = append(files, line{IndexFile, len(d.index)})
		}

		files = append(files, line{OwnerFile, len(d.owner)})
	}

	for _, n := range d.notes {
		if rel == "" || strings.HasPrefix(n.Path, rel+"/") {
			files = append(files, line{n.Path, int(n.Bytes.Int64)})
		}
	}

	total := 0
	dirs := map[string]int{}

	for _, f := range files {
		total += f.size

		if parent, _, ok := strings.Cut(f.path, "/"); ok && rel == "" {
			dirs[parent] += f.size
		}
	}

	var b strings.Builder

	fmt.Fprintf(&b, "Here're the files and directories up to 2 levels deep in %s, excluding hidden items and "+
		"node_modules:\n%s\t%s\n", full(rel), agenttool.HumanSize(total), full(rel))

	seen := map[string]bool{}

	for _, f := range files {
		if parent, _, ok := strings.Cut(f.path, "/"); ok && rel == "" && !seen[parent] {
			seen[parent] = true
			fmt.Fprintf(&b, "%s\t%s\n", agenttool.HumanSize(dirs[parent]), full(parent))
		}

		fmt.Fprintf(&b, "%s\t%s\n", agenttool.HumanSize(f.size), full(f.path))
	}

	return strings.TrimSuffix(b.String(), "\n")
}

// viewFile is a file's lines with their numbers, from view_range when given, cut at 16,000 characters.
func viewFile(rel, text string, viewRange []int) (string, error) {
	lines := splitLines(text)
	from, to := 1, len(lines)

	if len(viewRange) > 0 {
		if len(viewRange) != 2 || viewRange[0] < 1 || viewRange[0] > max(len(lines), 1) ||
			(viewRange[1] != -1 && viewRange[1] < viewRange[0]) {
			return "", refusef("Error: Invalid `view_range` parameter: %v. It should be [start_line, end_line] "+
				"within [1, %d], or [start_line, -1]", viewRange, len(lines))
		}

		from = viewRange[0]
		if viewRange[1] != -1 {
			to = min(viewRange[1], len(lines))
		}
	}

	var b strings.Builder

	fmt.Fprintf(&b, "Here's the content of %s with line numbers:\n", full(rel))

	used := utf8.RuneCountInString(b.String())

	for i := from; i <= to; i++ {
		row := fmt.Sprintf("%6d\t%s\n", i, lines[i-1])
		if used += utf8.RuneCountInString(row); used > maxViewChars {
			fmt.Fprintf(&b, "(cut at %s characters: read on with view_range [%d, -1])\n", thousands(maxViewChars), i)

			break
		}

		b.WriteString(row)
	}

	return strings.TrimSuffix(b.String(), "\n"), nil
}

// usage is how full the directory is after a change: MEMORY.md, USER.md when it was written, and the notes; from
// 80% of a limit it asks for room down to 70%, Coder's nudge (MemoryConsolidateThreshold), as MemoryUsage.
func (d dirState) usage(owner bool) string {
	var b strings.Builder

	part := func(name string, used, limit int, unit, ask string) {
		percent := used * 100 / limit
		fmt.Fprintf(&b, "%s: %s of %s %s (%d%%).", name, thousands(used), thousands(limit), unit, percent)

		if percent >= fullPercent {
			fmt.Fprintf(&b, " Nearly full: %s, until under %d%% (%s %s).", ask, targetPercent,
				thousands(limit*targetPercent/100), unit)
		}

		b.WriteString("\n")
	}

	if !d.ownerOnly {
		part(IndexFile, utf8.RuneCountInString(d.index), MaxMemory, "characters",
			"move detail into notes and keep one line for each in MEMORY.md, merge entries that say the same thing "+
				"and remove stale ones")
	}

	if owner {
		part(OwnerFile, utf8.RuneCountInString(d.owner), MaxOwnerMemory, "characters",
			"merge entries that say the same thing and remove stale ones")
	}

	if !d.ownerOnly {
		part("Notes", len(d.notes), MaxNotes, "files", "merge notes on one subject and delete stale ones")
	}

	return strings.TrimSuffix(b.String(), "\n")
}

// Coder's 80% and 70%, as agenttool's memoryFullPercent and memoryTargetPercent.
const (
	fullPercent   = 80
	targetPercent = 70
)

func thousands(n int) string {
	s := strconv.Itoa(n)

	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}

	return s
}

func deref(s *string) string {
	if s == nil {
		return ""
	}

	return *s
}

// readNoteIn is a note's text, for view.
func readNoteIn(ctx context.Context, q *store.Queries, dir Dir, rel string) (string, error) {
	row, err := q.GetMemoryNote(ctx, store.GetMemoryNoteParams{Kind: dir.kind, Name: dir.name, Path: rel})

	switch {
	case errors.Is(err, sql.ErrNoRows):
		return "", refusef("The path %s does not exist. Please provide a valid path.", full(rel))
	case err != nil:
		return "", err
	}

	return row.Text, nil
}
