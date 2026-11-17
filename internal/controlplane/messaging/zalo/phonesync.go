package zalo

// Syncing from the owner's phone (issue #697, step 4 of #613): Zalo's servers give this account no past (step 2,
// #669), so the past comes as Zalo PC's "Sync messages" brings it. The bridge asks the phone, follows its answer
// and downloads the backup (bridges/zalo/src/phonesync.mjs, after softtyos/zca-js); the control plane reads it
// (backup.go) and keeps every chat's messages as those of the past, the phone's ids turned into Zalo Web's
// (softtyos getNoisedIds; zlog maps them through its own service the same way).

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/messaging"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
)

const (
	// phoneAskWait bounds asking the phone; its answer comes later, as events.
	phoneAskWait = time.Minute
	// phoneIDBatch is how many ids one turning into Zalo Web's carries (inference: softtyos sets no limit).
	phoneIDBatch = 100
	// recalled is the type of a message taken back, which zlog leaves out (parser.go, SkipMsgTypes).
	recalled = 36
)

// phoneKinds are the backup's message types as the listener names them (zlog message_converter.go,
// MSG_TYPE_NAMES); a type not listed is a text.
var phoneKinds = map[int]string{
	2: "chat.doodle", 3: "chat.photo", 4: "chat.photo", 6: "chat.voice", 10: "chat.sticker",
	12: "chat.recommended", 15: "chat.notification", 18: "chat.location.new", 19: "chat.video.msg",
	22: "share.file", 23: "chat.gif", 26: "group.poll",
}

// errSyncing is a sync asked for while another is under way.
var errSyncing = fmt.Errorf("%w: a sync from the phone is under way; answer it on the phone",
	orchestration.ErrConflict)

// SyncFromPhone asks the owner's phone for its messages: the phone shows a confirmation, and what follows is told
// by the account's State (PhoneSync).
func (a *account) SyncFromPhone(ctx context.Context) error {
	b := a.current()
	if b == nil {
		return errNotConnected
	}

	a.mu.Lock()
	if s := a.sync; s != nil && (s.Stage == messaging.SyncAsking || s.Stage == messaging.SyncSending ||
		s.Stage == messaging.SyncImporting) {
		a.mu.Unlock()

		return errSyncing
	}
	a.mu.Unlock()

	dir := filepath.Join(a.svc.syncDir, a.uid)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("zalo: a folder for the phone's backup: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, phoneAskWait)
	defer cancel()

	// Set before asking: the phone's answer may come before the bridge's.
	a.setSync(messaging.PhoneSync{Stage: messaging.SyncAsking})

	if err := b.call(ctx, "phoneSync", map[string]any{"dir": dir}, nil); err != nil {
		a.setSync(messaging.PhoneSync{Stage: messaging.SyncFailed, Reason: err.Error()})

		return fmt.Errorf("zalo: asking the phone: %w", err)
	}

	return nil
}

func (a *account) setSync(s messaging.PhoneSync) {
	s.At = time.Now().UTC()

	a.mu.Lock()
	defer a.mu.Unlock()

	a.sync = &s
}

// onPhoneSync follows the phone's answer (bridge.mjs, onSync).
func (a *account) onPhoneSync(ctx context.Context, b *bridge, raw json.RawMessage) {
	var e struct {
		Stage   string `json:"stage"`
		Message string `json:"message"`
		Path    string `json:"path"`
		Key     string `json:"key"`
	}

	if json.Unmarshal(raw, &e) != nil {
		return
	}

	switch e.Stage {
	case "confirmed":
		// The phone's own word on where it is, when it gave one: preparing, or paused (bridge.mjs, onSync).
		a.setSync(messaging.PhoneSync{Stage: messaging.SyncSending, Reason: e.Message})
	case "refused":
		a.setSync(messaging.PhoneSync{Stage: messaging.SyncFailed, Reason: "it was refused on the phone"})
	case "failed":
		a.setSync(messaging.PhoneSync{Stage: messaging.SyncFailed, Reason: e.Message})
	case "ready":
		key, err := base64.StdEncoding.DecodeString(e.Key)
		if err != nil || !a.ownBackup(e.Path) {
			a.setSync(messaging.PhoneSync{Stage: messaging.SyncFailed, Reason: "the bridge gave a backup it cannot be"})

			return
		}

		a.setSync(messaging.PhoneSync{Stage: messaging.SyncImporting})

		go a.importBackup(ctx, b, e.Path, key)
	}
}

// ownBackup says a path the bridge gave is a file in the account's own folder for backups.
func (a *account) ownBackup(path string) bool {
	rel, err := filepath.Rel(filepath.Join(a.svc.syncDir, a.uid), path)

	return err == nil && rel != "." && !strings.Contains(rel, string(filepath.Separator)) && !strings.HasPrefix(rel, "..")
}

// importBackup keeps every chat of the backup, then removes it.
func (a *account) importBackup(ctx context.Context, b *bridge, path string, key []byte) {
	defer func() { _ = os.Remove(path) }()

	imp := &phoneImport{a: a, b: b, ids: map[string]string{}}

	err := eachDatabase(ctx, path, key, filepath.Dir(path), imp.chat)
	_ = os.Remove(path)

	if err != nil {
		a.svc.log.WarnContext(ctx, "zalo: the phone's backup could not be read", slog.String("account", a.uid),
			slog.String("error", err.Error()))
		a.setSync(messaging.PhoneSync{Stage: messaging.SyncFailed, Reason: err.Error(), Messages: imp.added,
			Chats: imp.chats, Skipped: imp.skipped})

		return
	}

	a.setSync(messaging.PhoneSync{Stage: messaging.SyncDone, Messages: imp.added, Chats: imp.chats,
		Skipped: imp.skipped})

	a.svc.log.InfoContext(ctx, "zalo: the phone's messages kept", slog.String("account", a.uid),
		slog.Int("messages", imp.added), slog.Int("chats", imp.chats), slog.Int("skipped", imp.skipped))

	// The direct chats it brought have no name yet.
	if a.naming.CompareAndSwap(false, true) {
		defer a.naming.Store(false)

		a.nameDirectChats(ctx, b)
	}
}

// phoneImport is one backup being kept: the ids turned so far, and what it added.
type phoneImport struct {
	a   *account
	b   *bridge
	ids map[string]string

	added, chats, skipped int
}

// chatOf reads a database's name as its chat: group_<id>.db and <id>.db (zlog GetThreadIDFromDatabaseName), and
// 4_<owner>_<group>.db and 3_<owner>_<person>.db (softtyos's guide, §5).
func chatOf(name string) (string, bool, bool) {
	stem := strings.TrimSuffix(name, ".db")
	if stem == name || stem == "" {
		return "", false, false
	}

	if id, ok := strings.CutPrefix(stem, "group_"); ok {
		return id, true, id != ""
	}

	if parts := strings.Split(stem, "_"); len(parts) == 3 && (parts[0] == "3" || parts[0] == "4") {
		return parts[2], parts[0] == "4", parts[2] != ""
	}

	if strings.Contains(stem, "_") {
		return "", false, false
	}

	return stem, false, true
}

// chat keeps one chat database's messages.
func (p *phoneImport) chat(ctx context.Context, file zdbFile, db string) error {
	raw, group, ok := chatOf(file.Name)
	if !ok {
		p.skipped++

		return nil
	}

	senders := map[string]bool{}
	if err := eachRow(ctx, db, func(r phoneRow) error {
		senders[r.Sender] = true

		return nil
	}); err != nil {
		p.a.svc.log.WarnContext(ctx, "zalo: a chat of the phone's backup could not be read",
			slog.String("file", file.Name), slog.String("error", err.Error()))

		p.skipped++

		return nil
	}

	var fids, gids []string
	if group {
		gids = append(gids, raw)
	} else {
		fids = append(fids, raw)
	}

	for s := range senders {
		fids = append(fids, s)
	}

	if err := p.turn(ctx, fids, gids); err != nil {
		return err
	}

	chat := p.web(raw, group)
	if chat == "" {
		p.skipped++

		return nil
	}

	added := 0
	err := eachRow(ctx, db, func(r phoneRow) error {
		m, keep := p.message(r, chat, group)
		if !keep {
			return nil
		}

		if ok, err := p.a.keepMessage(ctx, m); err != nil {
			return err
		} else if ok {
			added++
		}

		return nil
	})

	p.added += added
	if added > 0 {
		p.chats++
	}

	return err
}

// message is a row of the backup as the listener gives a message (protocol.mjs, message and media).
func (p *phoneImport) message(r phoneRow, chat string, group bool) (incoming, bool) {
	if r.Type == recalled || r.At == 0 {
		return incoming{}, false
	}

	id := strconv.FormatInt(r.GlobalID, 10)
	if r.GlobalID == 0 {
		id = localID(r.CliID, r.At)
	}

	from := p.web(r.Sender, false)
	if from == "" {
		from = r.Sender
	}

	fromMe := r.Sender == "0" || from == p.a.uid

	if fromMe {
		from = p.a.uid
	}

	kind := phoneKinds[r.Type]
	if kind == "" {
		kind = "webchat"
	}

	m := incoming{
		ID: id, CliID: strconv.FormatInt(r.CliID, 10), Chat: chat, Group: group, From: from, FromMe: fromMe,
		At: r.At, Kind: kind, Text: r.Text,
	}

	title, href := binNetValue(r.BinNet, tagTitle), binNetValue(r.BinNet, tagHref)

	switch kind {
	case "chat.photo", "chat.gif", "chat.doodle":
		m.Text, m.Media = title, &mediaOf{Type: "image", URL: href}
	case "chat.video.msg":
		m.Text, m.Media = title, &mediaOf{Type: "video", URL: href, Size: paramSize(r.BinNet)}
	case "chat.voice":
		m.Text, m.Media = "", &mediaOf{Type: "audio", URL: href, Size: paramSize(r.BinNet)}
	case "share.file":
		m.Text, m.Media = "", &mediaOf{Type: "document", URL: href, FileName: title, Size: paramSize(r.BinNet)}
	case "chat.sticker":
		m.Text, m.Media = "", &mediaOf{Type: "sticker"}
	case "webchat":
	default:
		if m.Text == "" {
			m.Text = strings.TrimSpace(title + "\n" + href)
		}
	}

	return m, true
}

// paramSize is a file's size in a BinNet's params, when they say it (zlog: params.fileSize).
func paramSize(b []byte) int64 {
	var params struct {
		FileSize json.Number `json:"fileSize"`
	}

	if json.Unmarshal([]byte(binNetValue(b, tagParams)), &params) != nil {
		return 0
	}

	n, _ := params.FileSize.Int64()

	return n
}

// localID is the id of a message the backup gives without Zalo's id (zlog generateDeterministicMsgId).
func localID(cli, at int64) string {
	h := fnv.New32a()
	_, _ = fmt.Fprintf(h, "%d|%d", cli, at)

	return fmt.Sprintf("local_%s_%d", strconv.FormatUint(uint64(h.Sum32()), 36), cli)
}

// web is the Zalo Web id of a phone's id, once turned: "0" is the owner; "" when Zalo did not know it.
func (p *phoneImport) web(raw string, group bool) string {
	if raw == "0" {
		return p.a.uid
	}

	return p.ids[idKey(raw, group)]
}

func idKey(raw string, group bool) string {
	if group {
		return "g" + raw
	}

	return "u" + raw
}

// turn asks for the Zalo Web ids of the phone's ids not turned yet. An id already Zalo Web's (a chat kept, the
// owner, or too long to be the phone's plain number) is its own.
func (p *phoneImport) turn(ctx context.Context, fids, gids []string) error {
	var askF, askG []string

	need := func(raw string, group bool) bool {
		k := idKey(raw, group)
		if _, done := p.ids[k]; done || raw == "" || raw == "0" {
			return false
		}

		if raw == p.a.uid || len(raw) >= 16 || p.a.svc.mirror.chatKind(ctx, p.a.uid, raw) != "" {
			p.ids[k] = raw

			return false
		}

		return true
	}

	for _, f := range fids {
		if need(f, false) {
			askF = append(askF, f)
		}
	}

	for _, g := range gids {
		if need(g, true) {
			askG = append(askG, g)
		}
	}

	for len(askF) > 0 || len(askG) > 0 {
		f, g := askF[:min(len(askF), phoneIDBatch)], askG[:min(len(askG), phoneIDBatch)]
		askF, askG = askF[len(f):], askG[len(g):]

		if err := p.ask(ctx, f, g); err != nil {
			return err
		}
	}

	return nil
}

func (p *phoneImport) ask(ctx context.Context, fids, gids []string) error {
	ctx, cancel := context.WithTimeout(ctx, keepTimeout)
	defer cancel()

	var got struct {
		Fids []string `json:"fids"`
		Gids []string `json:"gids"`
	}

	if err := p.b.call(ctx, "phoneIds", map[string]any{"fids": fids, "gids": gids}, &got); err != nil {
		return fmt.Errorf("zalo: turning the phone's ids into Zalo Web's: %w", err)
	}

	for i, f := range fids {
		if i < len(got.Fids) {
			p.ids[idKey(f, false)] = got.Fids[i]
		}
	}

	for i, g := range gids {
		if i < len(got.Gids) {
			p.ids[idKey(g, true)] = got.Gids[i]
		}
	}

	return nil
}
