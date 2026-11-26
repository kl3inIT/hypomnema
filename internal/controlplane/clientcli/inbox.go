package clientcli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
)

// The Inbox from the terminal (issue #966): what the Inbox's operations do, as every operation is a command
// (docs/conventions.md, API). A change reads the item first and sends the revision it read, so a change made
// meanwhile is refused as a conflict, not overwritten. Who acts is the token's, as in the API.

func inboxFlags(fs *flag.FlagSet) {
	fs.String("due", "", "when it is due: YYYY-MM-DD, YYYY-MM-DD HH:MM, none or unknown")
	fs.String("tz", "", "the IANA time zone of --due, such as Asia/Ho_Chi_Minh (default $TZ)")
	fs.String("who", "", "who does it: me, unknown, bot:<bot id> or a person's name")
	fs.String("notes", "", "notes")
	fs.String("done-when", "", "what done looks like")
	fs.String("priority", "", "how much it matters: none, urgent, high, medium or low (or 0 to 4)")
	fs.String("priority-reason", "", "why it has that priority, in a line, with --priority")
	fs.String("project", "", "the project it is in, a department by its name or id; none for no project")
}

// priorityNames are Linear's priorities, by their value 0 to 4.
var priorityNames = []string{"none", "urgent", "high", "medium", "low"}

// priorityOf reads --priority: a name or Linear's 0-4.
func priorityOf(s string) (int, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	for i, name := range priorityNames {
		if s == name || s == strconv.Itoa(i) {
			return i, nil
		}
	}

	return 0, fmt.Errorf("--priority %q is none, urgent, high, medium or low", s)
}

// projectOf is the id of the project --project names, a department of the sidebar (issue #1188) by its id or its
// name, in its case first and then in any case; none is "".
func projectOf(ctx context.Context, e *env, named string) (string, error) {
	named = strings.TrimSpace(named)
	if strings.EqualFold(named, "none") {
		return "", nil
	}

	resp, err := e.api.ListDepartmentsWithResponse(ctx)
	if err != nil {
		return "", fmt.Errorf("listing the departments: %w", err)
	}

	if resp.JSON200 == nil {
		return "", apiError("listing the departments", resp.StatusCode(), resp.JSONDefault)
	}

	for _, d := range resp.JSON200.Data {
		if d.Id == named || d.Name == named {
			return d.Id, nil
		}
	}

	names := make([]string, 0, len(resp.JSON200.Data))

	for _, d := range resp.JSON200.Data {
		if strings.EqualFold(d.Name, named) {
			return d.Id, nil
		}

		names = append(names, d.Name)
	}

	return "", fmt.Errorf("--project %q is none of the departments: %s", named, strings.Join(names, ", "))
}

func inboxList(ctx context.Context, e *env, fs *flag.FlagSet) error {
	p := &openapi.ListTodosParams{Cursor: optional(fs, "cursor")}

	if n := number(fs, "limit"); n > 0 {
		p.Limit = &n
	}

	if s := str(fs, "state"); s != "" {
		var states []openapi.TodoState
		for st := range strings.SplitSeq(s, ",") {
			states = append(states, openapi.TodoState(strings.TrimSpace(st)))
		}

		p.State = &states
	}

	if who := str(fs, "who"); who != "" {
		a := assignee(who)
		p.AssigneeKind, p.Assignee = &a.Kind, a.Name
	}

	if s := str(fs, "source"); s != "" {
		kind := openapi.TodoSourceKind(s)
		p.SourceKind = &kind
	}

	if s := str(fs, "project"); s != "" {
		id, err := projectOf(ctx, e, s)
		if err != nil {
			return err
		}

		if id == "" {
			id = "none"
		}

		p.Project = &id
	}

	for name, into := range map[string]**time.Time{"due-from": &p.DueFrom, "due-to": &p.DueTo} {
		if s := str(fs, name); s != "" {
			t, err := localTime(s)
			if err != nil {
				return fmt.Errorf("--%s: %w", name, err)
			}

			*into = &t
		}
	}

	for name, into := range map[string]**bool{
		"needs-you": &p.NeedsYou, "snoozed": &p.IncludeSnoozed, "closed": &p.IncludeClosed, "stale": &p.IncludeStale,
	} {
		if boolean(fs, name) {
			yes := true
			*into = &yes
		}
	}

	resp, err := e.api.ListTodosWithResponse(ctx, p)
	if err != nil {
		return fmt.Errorf("listing the inbox: %w", err)
	}

	if resp.JSON200 == nil {
		return apiError("listing the inbox", resp.StatusCode(), resp.JSONDefault)
	}

	list := resp.JSON200

	return e.print(fs, list, func() {
		if len(list.Data) == 0 {
			e.printf("No to-dos.\n")

			return
		}

		for _, t := range list.Data {
			e.printf("%s\n", todoLine(t))
		}

		if next := val(list.NextCursor); next != "" {
			e.printf("%d of %d; next page: --cursor %s\n", len(list.Data), list.Total, next)
		}
	})
}

// todoLine is one to-do on a line: its id, state, due, who does it and title.
func todoLine(t openapi.Todo) string {
	state := string(t.State)
	if t.Stale {
		state += " (stale)"
	}

	if t.SnoozedUntil != nil {
		state += " (snoozed)"
	}

	return fmt.Sprintf("%s  %-20s %-16s %-12s %s", t.Id, state, dueText(t.Due), who(t.Assignee), t.Title)
}

func dueText(d openapi.TodoDue) string {
	switch d.Kind {
	case openapi.TodoDueKindDate:
		if d.Date != nil {
			return d.Date.Format(time.DateOnly)
		}
	case openapi.TodoDueKindInstant:
		if d.At != nil {
			at := *d.At
			if loc, err := time.LoadLocation(val(d.Zone)); err == nil {
				at = at.In(loc)
			}

			return at.Format("2006-01-02 15:04")
		}
	case openapi.TodoDueKindUnknown:
		return "due, unknown"
	case openapi.TodoDueKindNone:
	}

	return "-"
}

func who(a openapi.TodoAssignee) string {
	switch a.Kind {
	case openapi.TodoAssigneeKindBot:
		return "bot:" + val(a.Name)
	case openapi.TodoAssigneeKindPerson:
		return val(a.Name)
	default:
		return string(a.Kind)
	}
}

func inboxShow(ctx context.Context, e *env, fs *flag.FlagSet) error {
	t, err := getTodo(ctx, e, fs.Arg(0))
	if err != nil {
		return err
	}

	return e.print(fs, t, func() { printTodo(e, t) })
}

func getTodo(ctx context.Context, e *env, id string) (openapi.Todo, error) {
	resp, err := e.api.GetTodoWithResponse(ctx, id)
	if err != nil {
		return openapi.Todo{}, fmt.Errorf("reading to-do %s: %w", id, err)
	}

	if resp.JSON200 == nil {
		return openapi.Todo{}, apiError("reading to-do "+id, resp.StatusCode(), resp.JSONDefault)
	}

	return *resp.JSON200, nil
}

func printTodo(e *env, t openapi.Todo) {
	e.printf("%s\n%s · %s · due %s · %s · revision %d\n", t.Title, t.Id, t.State, dueText(t.Due), who(t.Assignee),
		t.Revision)

	src := string(t.Source.Kind)
	if id := val(t.Source.Id); id != "" {
		src += " " + id
	}

	if t.WhyOpen != nil {
		src += ", open: " + string(*t.WhyOpen)
	}

	e.printf("From: %s\n", src)

	if t.Priority > 0 && t.Priority < len(priorityNames) {
		e.printf("Priority: %s", priorityNames[t.Priority])

		if t.PriorityReason != "" {
			e.printf(", %s", t.PriorityReason)
		}

		e.printf("\n")
	}

	if id := val(t.ProjectId); id != "" {
		e.printf("Project: %s\n", id)
	}

	if q := val(t.Source.Quote); q != "" {
		e.printf("  %q\n", q)
	}

	if t.SnoozedUntil != nil {
		e.printf("Snoozed until %s\n", t.SnoozedUntil.Local().Format("2006-01-02 15:04"))
	}

	if t.Stale {
		e.printf("Stale: proposed and untouched for three days\n")
	}

	if t.DoneWhen != "" {
		e.printf("Done when: %s\n", t.DoneWhen)
	}

	if t.Notes != "" {
		e.printf("\n%s\n", t.Notes)
	}

	if t.Work != nil {
		e.printf("Doing: bot %s in %s %s (attempt %s)\n", t.Work.Bot, t.Work.Kind, t.Work.Id, t.Work.Attempt)
	}

	if t.ClosedAt != nil {
		e.printf("Closed %s", t.ClosedAt.Local().Format("2006-01-02 15:04"))

		if t.CloseReason != "" {
			e.printf(": %s", t.CloseReason)
		}

		e.printf("\n")
	}

	if of := val(t.DuplicateOf); of != "" {
		e.printf("Duplicate of %s\n", of)
	}

	for _, l := range t.Evidence {
		e.printf("  evidence %s %s %s\n", l.Kind, l.Ref, val(l.Label))
	}

	if t.Events == nil {
		return
	}

	e.printf("\nHistory:\n")

	for _, ev := range *t.Events {
		actor := string(ev.Actor.Kind)
		if id := val(ev.Actor.Id); id != "" {
			actor += " " + id
		}

		e.printf("  %s  %-8s %s\n", ev.At.Local().Format("2006-01-02 15:04"), ev.Kind, actor)
	}
}

func inboxAdd(ctx context.Context, e *env, fs *flag.FlagSet) error {
	n := openapi.NewTodo{Title: fs.Arg(0), Notes: optional(fs, "notes"), DoneWhen: optional(fs, "done-when")}

	if s := str(fs, "due"); s != "" {
		due, err := dueOf(s, str(fs, "tz"))
		if err != nil {
			return err
		}

		n.Due = &due
	}

	if s := str(fs, "who"); s != "" {
		a := assignee(s)
		n.Assignee = &a
	}

	if s := str(fs, "priority"); s != "" {
		p, err := priorityOf(s)
		if err != nil {
			return err
		}

		n.Priority, n.PriorityReason = &p, optional(fs, "priority-reason")
	}

	if s := str(fs, "project"); s != "" {
		id, err := projectOf(ctx, e, s)
		if err != nil {
			return err
		}

		if id != "" {
			n.ProjectId = &id
		}
	}

	resp, err := e.api.CreateTodoWithResponse(ctx, &openapi.CreateTodoParams{IdempotencyKey: key(fs)}, n)
	if err != nil {
		return fmt.Errorf("adding a to-do: %w", err)
	}

	if resp.JSON201 == nil {
		return apiError("adding a to-do", resp.StatusCode(), resp.JSONDefault)
	}

	return printTodoLine(e, fs, *resp.JSON201)
}

func printTodoLine(e *env, fs *flag.FlagSet, t openapi.Todo) error {
	return e.print(fs, t, func() { e.printf("%s\n", todoLine(t)) })
}

func inboxEdit(ctx context.Context, e *env, fs *flag.FlagSet) error {
	t, err := getTodo(ctx, e, fs.Arg(0))
	if err != nil {
		return err
	}

	b := openapi.TodoEdit{Revision: t.Revision}

	fs.Visit(func(f *flag.Flag) {
		v := f.Value.String()

		switch f.Name {
		case "title":
			b.Title = &v
		case "notes":
			b.Notes = &v
		case "done-when":
			b.DoneWhen = &v
		case "who":
			a := assignee(v)
			b.Assignee = &a
		}
	})

	if s := str(fs, "due"); s != "" {
		due, err := dueOf(s, str(fs, "tz"))
		if err != nil {
			return err
		}

		b.Due = &due
	}

	if s := str(fs, "priority"); s != "" {
		p, err := priorityOf(s)
		if err != nil {
			return err
		}

		b.Priority, b.PriorityReason = &p, optional(fs, "priority-reason")
	}

	if s := str(fs, "project"); s != "" {
		id, err := projectOf(ctx, e, s)
		if err != nil {
			return err
		}

		b.ProjectId = &id
	}

	if b == (openapi.TodoEdit{Revision: t.Revision}) {
		return errors.New("nothing to change: give --title, --notes, --done-when, --due, --who, --priority or " +
			"--project")
	}

	resp, err := e.api.EditTodoWithResponse(ctx, t.Id, b)
	if err != nil {
		return fmt.Errorf("changing to-do %s: %w", t.Id, err)
	}

	if resp.JSON200 == nil {
		return apiError("changing to-do "+t.Id, resp.StatusCode(), resp.JSONDefault)
	}

	return printTodoLine(e, fs, *resp.JSON200)
}

// change reads a to-do and sends one change at the revision it read.
func change[R interface{ StatusCode() int }](
	ctx context.Context, e *env, fs *flag.FlagSet, doing string,
	send func(t openapi.Todo) (R, error), answer func(R) (*openapi.Todo, *openapi.Error),
) error {
	t, err := getTodo(ctx, e, fs.Arg(0))
	if err != nil {
		return err
	}

	resp, err := send(t)
	if err != nil {
		return fmt.Errorf("%s %s: %w", doing, t.Id, err)
	}

	got, fail := answer(resp)
	if got == nil {
		return apiError(doing+" "+t.Id, resp.StatusCode(), fail)
	}

	return printTodoLine(e, fs, *got)
}

func inboxAccept(ctx context.Context, e *env, fs *flag.FlagSet) error {
	return change(ctx, e, fs, "accepting to-do",
		func(t openapi.Todo) (*openapi.AcceptTodoResponse, error) {
			return e.api.AcceptTodoWithResponse(ctx, t.Id, openapi.TodoChange{Revision: t.Revision})
		},
		func(r *openapi.AcceptTodoResponse) (*openapi.Todo, *openapi.Error) { return r.JSON200, r.JSONDefault })
}

func inboxDone(ctx context.Context, e *env, fs *flag.FlagSet) error {
	b := openapi.TodoDone{Reason: optional(fs, "reason")}

	if url := str(fs, "evidence"); url != "" {
		b.Evidence = &[]openapi.TodoLink{{Kind: openapi.TodoLinkKindUrl, Ref: url}}
	}

	return change(ctx, e, fs, "closing to-do",
		func(t openapi.Todo) (*openapi.DoneTodoResponse, error) {
			b.Revision = t.Revision

			return e.api.DoneTodoWithResponse(ctx, t.Id, b)
		},
		func(r *openapi.DoneTodoResponse) (*openapi.Todo, *openapi.Error) { return r.JSON200, r.JSONDefault })
}

func inboxDrop(ctx context.Context, e *env, fs *flag.FlagSet) error {
	return change(ctx, e, fs, "dropping to-do",
		func(t openapi.Todo) (*openapi.DropTodoResponse, error) {
			return e.api.DropTodoWithResponse(ctx, t.Id, openapi.TodoDrop{Revision: t.Revision, Reason: fs.Arg(1)})
		},
		func(r *openapi.DropTodoResponse) (*openapi.Todo, *openapi.Error) { return r.JSON200, r.JSONDefault })
}

func inboxDuplicate(ctx context.Context, e *env, fs *flag.FlagSet) error {
	return change(ctx, e, fs, "marking to-do",
		func(t openapi.Todo) (*openapi.DuplicateTodoResponse, error) {
			return e.api.DuplicateTodoWithResponse(ctx, t.Id, openapi.TodoDuplicate{Revision: t.Revision, Of: fs.Arg(1)})
		},
		func(r *openapi.DuplicateTodoResponse) (*openapi.Todo, *openapi.Error) {
			return r.JSON200, r.JSONDefault
		})
}

func inboxSnooze(ctx context.Context, e *env, fs *flag.FlagSet) error {
	b := openapi.TodoSnooze{}

	if s := fs.Arg(1); s != "off" {
		until, err := localTime(s)
		if err != nil {
			return fmt.Errorf("until: %w", err)
		}

		b.Until = &until
	}

	return change(ctx, e, fs, "snoozing to-do",
		func(t openapi.Todo) (*openapi.SnoozeTodoResponse, error) {
			b.Revision = t.Revision

			return e.api.SnoozeTodoWithResponse(ctx, t.Id, b)
		},
		func(r *openapi.SnoozeTodoResponse) (*openapi.Todo, *openapi.Error) { return r.JSON200, r.JSONDefault })
}

func inboxReopen(ctx context.Context, e *env, fs *flag.FlagSet) error {
	return change(ctx, e, fs, "reopening to-do",
		func(t openapi.Todo) (*openapi.ReopenTodoResponse, error) {
			return e.api.ReopenTodoWithResponse(ctx, t.Id, openapi.TodoChange{Revision: t.Revision})
		},
		func(r *openapi.ReopenTodoResponse) (*openapi.Todo, *openapi.Error) { return r.JSON200, r.JSONDefault })
}

// assignee reads --who: me, unknown, bot:<id>, or a person's name.
func assignee(s string) openapi.TodoAssignee {
	switch s = strings.TrimSpace(s); {
	case s == "me":
		return openapi.TodoAssignee{Kind: openapi.TodoAssigneeKindMe}
	case s == "unknown":
		return openapi.TodoAssignee{Kind: openapi.TodoAssigneeKindUnknown}
	case strings.HasPrefix(s, "bot:"):
		id := strings.TrimPrefix(s, "bot:")

		return openapi.TodoAssignee{Kind: openapi.TodoAssigneeKindBot, Name: &id}
	default:
		return openapi.TodoAssignee{Kind: openapi.TodoAssigneeKindPerson, Name: &s}
	}
}

// Layouts a person types a time in, in the machine's time zone unless the time names its offset.
var timeLayouts = []string{time.RFC3339, "2006-01-02T15:04", "2006-01-02 15:04", time.DateOnly}

// localTime reads a moment: RFC 3339, or a date and a time, or a date's start, in the machine's time zone.
func localTime(s string) (time.Time, error) {
	for _, layout := range timeLayouts {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t, nil
		}
	}

	return time.Time{}, fmt.Errorf("%q is not YYYY-MM-DD, YYYY-MM-DD HH:MM or RFC 3339", s)
}

// dueOf reads --due: none, unknown, a calendar date, or a date and a time, in the IANA zone tz (default $TZ). A date
// stays a date: no time is made up for it.
func dueOf(s, tz string) (openapi.TodoDue, error) {
	switch s {
	case "none":
		return openapi.TodoDue{Kind: openapi.TodoDueKindNone}, nil
	case "unknown":
		return openapi.TodoDue{Kind: openapi.TodoDueKindUnknown}, nil
	}

	if tz == "" {
		tz = os.Getenv("TZ")
	}

	loc, err := time.LoadLocation(tz)
	if tz == "" || err != nil {
		return openapi.TodoDue{}, fmt.Errorf("--due needs --tz, an IANA time zone such as Asia/Ho_Chi_Minh")
	}

	if d, err := time.Parse(time.DateOnly, s); err == nil {
		return openapi.TodoDue{Kind: openapi.TodoDueKindDate, Date: &openapi_types.Date{Time: d}, Zone: &tz}, nil
	}

	for _, layout := range timeLayouts[:3] {
		if t, err := time.ParseInLocation(layout, s, loc); err == nil {
			return openapi.TodoDue{Kind: openapi.TodoDueKindInstant, At: &t, Zone: &tz}, nil
		}
	}

	return openapi.TodoDue{}, fmt.Errorf("--due %q is not YYYY-MM-DD, YYYY-MM-DD HH:MM, none or unknown", s)
}
