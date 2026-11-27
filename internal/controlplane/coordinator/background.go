package coordinator

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
)

// A Bot's background commands (issues #1058, #1084, orchestration/background.go). Each runs as a systemd user unit
// of the coordinator user, outside the control plane's cgroup, so a restart of the control plane, which stops every
// process of its service and every pi with them, leaves it running: OpenClaw keeps a durable command across Gateway
// restarts as a transient systemd user service (docs/tools/exec.md:94-100, docs/gateway/background-process.md:
// 140-235, src/agents/bash-process-systemd.ts). The control plane starts, observes, stops and lets go of the units
// through the launcher deploy/coordinator/hypomnema-background, run as the coordinator; pi's box never reaches the
// user manager. As OpenClaw's registry it observes every 5 s while a command runs (src/agents/
// bash-process-registry.ts:809-814); a command that ended wakes its chat with its outcome and the end of its output,
// then its unit is let go of, the outcome being recorded first (bash-process-durable.ts:445-449). A command whose
// unit is gone is reported lost (bash-process-durable.ts:420-425), and a unit no row names is let go of. When the
// control plane is back after a restart, the first pass finds every command again by its unit's name, which is its
// chat's id and its own.

// Bounds of the background commands.
const (
	// backgroundEvery is how often the units are observed while a command runs (OpenClaw's 5 s).
	backgroundEvery = 5 * time.Second
	// backgroundWithin bounds one call of the launcher.
	backgroundWithin = 30 * time.Second
	// DefaultBackgroundTimeout is a command's deadline when its call gives none, in seconds: OpenClaw's
	// tools.exec.timeoutSeconds (docs/tools/exec.md:192), as pi-background.mts has always given.
	DefaultBackgroundTimeout = 1800
	// maxBackgroundTimeout is the longest deadline, in seconds: pi's bash's own (core/tools/bash.ts:21-24).
	maxBackgroundTimeout = 2_147_483
	// maxBackgroundCommand bounds a command's text, which pi's shell is given as one argument, as pi's bash gives it:
	// Linux takes at most 128 KiB in one (MAX_ARG_STRLEN). The launcher's read gives the output's last 8,000 bytes:
	// 2,000 characters are at most 8,000 bytes of UTF-8.
	maxBackgroundCommand = 120 << 10
	// backgroundPruneEvery is how often the output files of every chat's ended commands are pruned, which the
	// launcher does past OpenClaw's 3 hours (bash-process-registry.ts:24-26), whether a chat runs another command or
	// not.
	backgroundPruneEvery = 10 * time.Minute
	// defaultBackgroundShell runs a command when pi names no shell: pi's own default on Linux (getShellConfig).
	defaultBackgroundShell = "/bin/bash"
)

// plainPath is a shell's path as the launcher takes it.
var plainPath = regexp.MustCompile(`^/[A-Za-z0-9._/+-]{1,255}$`)

// errNoBackground refuses a command when no launcher is configured: OpenClaw never falls back to a command that a
// restart would end (docs/tools/exec.md:94).
var errNoBackground = fmt.Errorf("%w: this control plane runs no background commands "+
	"(HYPOMNEMA_BACKGROUND_COMMAND is not set); run the command without background", orchestration.ErrUnavailable)

// backgroundRunner runs the launcher with args and stdin and returns what it printed.
type backgroundRunner func(ctx context.Context, args []string, stdin string) ([]byte, error)

// background is the supervisor's part that runs the commands. One call at a time: a command being started is never
// taken for one whose unit is gone.
type background struct {
	mu   sync.Mutex
	run  backgroundRunner
	last time.Time
	// listed says the units were listed once since the start, which lets go of those no row names; seen is how many
	// units the last list had, so that the units left when the last rows went, as with a deleted chat, are listed
	// and let go of too.
	listed bool
	seen   int
	// pruned is when the ended commands' files were last pruned.
	pruned time.Time
}

// backgroundLauncher runs command, the launcher's, as the control plane's other launchers are run (chatfiles.go).
func backgroundLauncher(command []string) backgroundRunner {
	return func(ctx context.Context, args []string, stdin string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(ctx, backgroundWithin)
		defer cancel()

		all := append(append([]string{}, command[1:]...), args...)
		cmd := exec.CommandContext(ctx, command[0], all...) //nolint:gosec // the owner's own command

		var stdout, stderr bytes.Buffer

		cmd.Stdin = strings.NewReader(stdin)
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr

		if err := cmd.Run(); err != nil {
			why := strings.TrimSpace(stderr.String())
			if len(why) > 2000 {
				why = why[:2000]
			}

			return nil, fmt.Errorf("%w: the background launcher's %s failed: %w: %s", orchestration.ErrUnavailable,
				args[0], err, why)
		}

		return stdout.Bytes(), nil
	}
}

// StartBackground records the command the pi of the chat whose token hashes to pi asked for, and starts its unit
// as the chat's Bot. A command that cannot start leaves no row.
func (s *Supervisor) StartBackground(ctx context.Context, chatID, bot, pi string, in agenttool.BackgroundRequest,
) (agenttool.BackgroundAnswer, error) {
	if s.bg.run == nil {
		return agenttool.BackgroundAnswer{}, errNoBackground
	}

	timeout := in.TimeoutSeconds
	if timeout == 0 {
		timeout = DefaultBackgroundTimeout
	}

	switch {
	case timeout < 0 || timeout > maxBackgroundTimeout:
		return agenttool.BackgroundAnswer{}, fmt.Errorf("%w: a background command's timeout is 1 to %d seconds",
			orchestration.ErrInvalid, maxBackgroundTimeout)
	case strings.TrimSpace(in.Command) == "" || len(in.Command) > maxBackgroundCommand:
		return agenttool.BackgroundAnswer{}, fmt.Errorf("%w: a background command is 1 to %d bytes",
			orchestration.ErrInvalid, maxBackgroundCommand)
	case !strings.HasPrefix(in.Cwd, "/") || strings.ContainsAny(in.Cwd, "\n\r\x00"):
		return agenttool.BackgroundAnswer{}, fmt.Errorf("%w: a background command starts in an absolute path",
			orchestration.ErrInvalid)
	}

	shell := in.Shell
	if shell == "" {
		shell = defaultBackgroundShell
	}

	if !plainPath.MatchString(shell) || strings.Contains(shell, "/../") {
		return agenttool.BackgroundAnswer{}, fmt.Errorf("%w: a background command's shell is a plain absolute path",
			orchestration.ErrInvalid)
	}

	id := newBackgroundID()

	// The unit outlives the request: a pi that ends while it starts leaves a command that still wakes its chat.
	ctx = context.WithoutCancel(ctx)

	s.bg.mu.Lock()
	defer s.bg.mu.Unlock()

	if err := s.svc.StartBackground(ctx, chatID, pi, id); err != nil {
		return agenttool.BackgroundAnswer{}, err
	}

	out, err := s.bg.run(ctx, []string{"start", chatID, bot, id, strconv.Itoa(timeout), in.Cwd, shell}, in.Command)
	if err != nil {
		if ferr := s.svc.ForgetBackground(ctx, chatID, id); ferr != nil {
			err = errors.Join(err, ferr)
		}

		return agenttool.BackgroundAnswer{}, err
	}

	s.cfg.Logger.InfoContext(ctx, "a background command started", slog.String("event", "coordinator.background_started"),
		slog.String("chat_id", chatID), slog.String("id", id), slog.Int("timeout_seconds", timeout))

	return agenttool.BackgroundAnswer{ID: id, OutputFile: lastLine(out), TimeoutSeconds: timeout}, nil
}

// StopBackground stops a command of the chat and wakes it with what the command wrote; false when the command is not
// running, as it ended already.
func (s *Supervisor) StopBackground(ctx context.Context, chatID, id string) (bool, error) {
	if s.bg.run == nil {
		return false, errNoBackground
	}

	if !orchestration.IsBackgroundID(id) {
		return false, fmt.Errorf("%w: a background command's id is bg_ and 6 to 32 lowercase letters or digits",
			orchestration.ErrInvalid)
	}

	ctx = context.WithoutCancel(ctx)

	s.bg.mu.Lock()
	defer s.bg.mu.Unlock()

	rows, err := s.svc.BackgroundCommands(ctx)
	if err != nil {
		return false, err
	}

	found := false

	for _, r := range rows {
		found = found || (r.ChatID == chatID && r.ID == id)
	}

	if !found {
		return false, nil
	}

	// One that ended and was not observed yet is reported as it ended, one whose unit is gone as lost.
	out, err := s.bg.run(ctx, []string{"list"}, "")
	if err != nil {
		return false, err
	}

	u, ok := parseUnits(out)[unitKey{chatID, id}]

	switch {
	case !ok:
		return false, s.lose(ctx, chatID, id)
	case !u.running():
		return false, s.collect(ctx, chatID, id, u, false)
	}

	if _, err := s.bg.run(ctx, []string{"stop", chatID, id}, ""); err != nil {
		return false, err
	}

	return true, s.collect(ctx, chatID, id, unitState{}, true)
}

// observeBackground wakes the chats whose commands ended, reports lost those whose unit is gone and lets go of the
// units no row names. It lists the units at most every backgroundEvery, and only while a command runs, once after a
// start, or while the last list had units. Every backgroundPruneEvery it prunes the ended commands' files.
func (s *Supervisor) observeBackground(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}

	s.bg.mu.Lock()
	defer s.bg.mu.Unlock()

	if s.bg.run == nil {
		return
	}

	if time.Since(s.bg.last) < backgroundEvery {
		return
	}

	s.bg.last = time.Now()

	if time.Since(s.bg.pruned) >= backgroundPruneEvery {
		s.bg.pruned = s.bg.last
		if _, err := s.bg.run(ctx, []string{"prune"}, ""); err != nil {
			s.backgroundFailed(ctx, "pruning background output files failed", "", err)
		}
	}

	rows, err := s.svc.BackgroundCommands(ctx)
	if err != nil {
		s.backgroundFailed(ctx, "listing background commands failed", "", err)

		return
	}

	if len(rows) == 0 && s.bg.listed && s.bg.seen == 0 {
		return
	}

	out, err := s.bg.run(ctx, []string{"list"}, "")
	if err != nil {
		s.backgroundFailed(ctx, "listing background units failed", "", err)

		return
	}

	s.bg.listed = true
	units := parseUnits(out)
	s.bg.seen = len(units)

	for _, r := range rows {
		u, ok := units[unitKey{r.ChatID, r.ID}]
		delete(units, unitKey{r.ChatID, r.ID})

		var err error

		switch {
		case ok && u.running():
			continue
		case ok:
			err = s.collect(ctx, r.ChatID, r.ID, u, false)
		default:
			err = s.lose(ctx, r.ChatID, r.ID)
		}

		if err != nil {
			s.backgroundFailed(ctx, "settling a background command failed", r.ChatID, err)
		}
	}

	// A unit whose row is gone: its chat was deleted, or the control plane stopped between the wake and the let-go.
	// Its output file stays for prune, as a wake may name it.
	for k := range units {
		if _, err := s.bg.run(ctx, []string{"cleanup", k.chat, k.id, "keep"}, ""); err != nil {
			s.backgroundFailed(ctx, "letting go of a background unit failed", k.chat, err)
		}
	}
}

// collect wakes the chat for a command that ended, with its output's end, then lets go of its unit. The output file
// stays when the wake shows only its end.
func (s *Supervisor) collect(ctx context.Context, chatID, id string, u unitState, stopped bool) error {
	out, err := s.bg.run(ctx, []string{"read", chatID, id}, "")
	if err != nil {
		return err
	}

	o := parseOutput(out)
	end := u.end(id)
	end.Stopped = stopped
	end.Output, end.OutputBytes = o.tail, o.size

	if o.cut {
		end.OutputFile = o.file
	}

	if _, err := s.svc.EndBackground(ctx, chatID, end); err != nil {
		return err
	}

	s.cfg.Logger.InfoContext(ctx, "a background command ended", slog.String("event", "coordinator.background_ended"),
		slog.String("chat_id", chatID), slog.String("id", id), slog.Bool("stopped", stopped),
		slog.String("result", u.result))

	_, err = s.bg.run(ctx, []string{"cleanup", chatID, id, keepIf(o.cut)}, "")

	return err
}

// lose reports lost a command whose unit is gone, naming its output when it wrote any.
func (s *Supervisor) lose(ctx context.Context, chatID, id string) error {
	out, err := s.bg.run(ctx, []string{"read", chatID, id}, "")
	if err != nil {
		return err
	}

	o := parseOutput(out)

	file := ""
	if o.size > 0 {
		file = o.file
	}

	if _, err := s.svc.LoseBackground(ctx, chatID, id, "its unit is no longer there, as after a restart of the "+
		"machine", file); err != nil {
		return err
	}

	s.cfg.Logger.WarnContext(ctx, "a background command's unit is gone",
		slog.String("event", "coordinator.background_lost"), slog.String("chat_id", chatID), slog.String("id", id))

	_, err = s.bg.run(ctx, []string{"cleanup", chatID, id, keepIf(file != "")}, "")

	return err
}

func (s *Supervisor) backgroundFailed(ctx context.Context, msg, chatID string, err error) {
	if ctx.Err() != nil {
		return
	}

	s.cfg.Logger.ErrorContext(ctx, msg, slog.String("event", "coordinator.background_failed"),
		slog.String("chat_id", chatID), slog.Any("error", err))
}

func keepIf(keep bool) string {
	if keep {
		return "keep"
	}

	return "drop"
}

func newBackgroundID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)

	return "bg_" + hex.EncodeToString(b)
}

func lastLine(out []byte) string {
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")

	return strings.TrimSpace(lines[len(lines)-1])
}

// unitKey names a command by its chat and its id, as its unit's name does: hypomnema-bg-<chat>-<id>.service.
type unitKey struct{ chat, id string }

// unitState is what systemd says of a unit (`systemctl show`), as OpenClaw reads it (bash-process-systemd.ts:
// 125-168).
type unitState struct {
	active, sub, result string
	// code is ExecMainCode, how the main process ended (1 exited, 2 killed, 3 dumped core); status its exit code or
	// signal.
	code, status int
	// started and exited are its main process's start and exit on the monotonic clock, in microseconds.
	started, exited int64
}

func (u unitState) running() bool {
	switch u.active {
	case "activating", "deactivating", "reloading":
		return true
	case "active":
		return u.sub != "exited"
	}

	return false
}

// end is the command's outcome as its unit says it: a signal when systemd saw one, else an exit code, and how long
// it ran by the unit's own clock, which a wait for the control plane does not lengthen (OpenClaw's supervisor-
// measured duration, bash-process-durable.ts:435-438). A stopped unit, which systemd let go of, says nothing: it
// was ended by SIGTERM.
func (u unitState) end(id string) orchestration.BackgroundEnd {
	e := orchestration.BackgroundEnd{ID: id, TimedOut: u.result == "timeout"}

	switch {
	case u.code == 2 || u.code == 3:
		e.Signal = signalName(u.status)
	case u.code == 1:
		e.ExitCode = &u.status
	case u.active == "":
		e.Signal = "SIGTERM"
	default:
		code := 0
		if u.result != "success" {
			code = 1
		}

		e.ExitCode = &code
	}

	if u.started > 0 && u.exited > u.started {
		e.Ran = time.Duration(u.exited-u.started) * time.Microsecond
	}

	return e
}

// parseUnits reads `systemctl show`'s blocks of key=value lines, one block a unit, keeping the background units.
func parseUnits(out []byte) map[unitKey]unitState {
	units := map[unitKey]unitState{}

	for block := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n\n") {
		var (
			key unitKey
			u   unitState
		)

		for line := range strings.SplitSeq(block, "\n") {
			name, value, _ := strings.Cut(strings.TrimSpace(line), "=")

			switch name {
			case "Id":
				key = unitKeyOf(value)
			case "ActiveState":
				u.active = value
			case "SubState":
				u.sub = value
			case "Result":
				u.result = value
			case "ExecMainCode":
				u.code, _ = strconv.Atoi(value)
			case "ExecMainStatus":
				u.status, _ = strconv.Atoi(value)
			case "ExecMainStartTimestampMonotonic":
				u.started, _ = strconv.ParseInt(value, 10, 64)
			case "ExecMainExitTimestampMonotonic":
				u.exited, _ = strconv.ParseInt(value, 10, 64)
			}
		}

		if key.chat != "" {
			units[key] = u
		}
	}

	return units
}

// unitKeyOf reads a unit's name, <prefix>-<chat>-<id>.service, where the launcher's prefix is hypomnema-bg; the zero
// key when it is not a background command's.
func unitKeyOf(name string) unitKey {
	rest, ok := strings.CutSuffix(name, ".service")
	if !ok {
		return unitKey{}
	}

	at := strings.LastIndex(rest, "-cht_")
	if at < 0 {
		return unitKey{}
	}

	chat, id, ok := strings.Cut(rest[at+1:], "-")
	if !ok || !orchestration.IsBackgroundID(id) {
		return unitKey{}
	}

	return unitKey{chat, id}
}

// output is what the launcher's read printed: the output file, its size, and its end, at most BackgroundTail
// characters; cut says the end is not all of it.
type output struct {
	file, tail string
	size       int64
	cut        bool
}

func parseOutput(out []byte) output {
	file, rest, _ := bytes.Cut(out, []byte("\n"))
	size, rest, _ := bytes.Cut(rest, []byte("\n"))

	o := output{file: strings.TrimSpace(string(file))}
	o.size, _ = strconv.ParseInt(strings.TrimSpace(string(size)), 10, 64)

	tail := rest
	if int64(len(tail)) < o.size {
		// The read began inside a character: its continuation bytes are not text.
		for len(tail) > 0 && !utf8.RuneStart(tail[0]) {
			tail = tail[1:]
		}

		o.cut = true
	}

	text := strings.ToValidUTF8(string(tail), "�")
	if n := utf8.RuneCountInString(text); n > orchestration.BackgroundTail {
		text = string([]rune(text)[n-orchestration.BackgroundTail:])
		o.cut = true
	}

	o.tail = text

	return o
}

// signalName names a signal by its number on Linux, where the units run.
func signalName(n int) string {
	names := map[int]string{
		1: "SIGHUP", 2: "SIGINT", 3: "SIGQUIT", 4: "SIGILL", 5: "SIGTRAP", 6: "SIGABRT", 7: "SIGBUS", 8: "SIGFPE",
		9: "SIGKILL", 10: "SIGUSR1", 11: "SIGSEGV", 12: "SIGUSR2", 13: "SIGPIPE", 14: "SIGALRM", 15: "SIGTERM",
		24: "SIGXCPU", 25: "SIGXFSZ",
	}
	if name, ok := names[n]; ok {
		return name
	}

	return "SIG" + strconv.Itoa(n)
}
