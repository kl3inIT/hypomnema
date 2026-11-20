package orchestration

import (
	"bytes"
	"database/sql"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway"
	"github.com/kl3inIT/hypomnema/internal/controlplane/vault"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
)

// credentialWorld is a world wired as the control plane wires it for credentials (issue #996): the gateway's
// revocations, a vault for share links' tokens, and a gateway that makes real keys.
func credentialWorld(t *testing.T) (*world, *modelgateway.Service, *vault.Service) {
	t.Helper()

	w := newWorld(t)
	w.s.gatewayKeys = modelgateway.Revocations{}
	secrets := testVault(t, w.db)
	w.s.WithShareVault(secrets.Copies())

	return w, modelgateway.New(w.db, w.clock, platform.NewID, nil, nil), secrets
}

func testVault(t *testing.T, db *sql.DB) *vault.Service {
	t.Helper()

	return testVaultWith(db, testKeys(t))
}

func testKeys(t *testing.T) *vault.Keyring {
	t.Helper()

	entry, err := vault.GenerateKey("k1")
	require.NoError(t, err)
	keys, err := vault.ParseKeyring(entry, "k1")
	require.NoError(t, err)

	return keys
}

// testVaultWith is a vault over the database with the keyring; nil is a vault without its keys.
func testVaultWith(db *sql.DB, keys *vault.Keyring) *vault.Service {
	return vault.New(db, platform.SystemClock{}, platform.NewID, keys, nil)
}

// shareOf shows an artifact in the chat and makes a link to it.
func (w *world) shareOf(chatID, bot string) ArtifactShare {
	w.t.Helper()

	a, _, err := w.s.ShowArtifact(w.t.Context(), ArtifactMaker{Bot: bot, Chat: chatID},
		ShowArtifact{Title: "Sales", Pack: page(w.t, "<p>sales")})
	require.NoError(w.t, err)

	sh, err := w.s.CreateArtifactShare(w.t.Context(), a.ID, "Laura")
	require.NoError(w.t, err)

	return sh
}

// revocation reads a credential's revoke reason from its table; "" for a live one.
func (w *world) revocation(table, id string) string {
	w.t.Helper()

	var reason sql.NullString
	require.NoError(w.t, w.db.QueryRowContext(w.t.Context(),
		"SELECT revoke_reason FROM "+table+" WHERE id = ? AND revoked_at IS NOT NULL", id).Scan(&reason))

	return reason.String
}

// Scenario (issue #996, rule 2 of #995): the owner makes a link. Its token is nowhere in the database in plaintext:
// the link keeps its SHA-256 and a copy sealed in the vault. The link opens by its token, and the owner reads it
// again from both lists; the vault names it as its sealed copy's user. Revoked, it is kept with the reason and its sealed copy is gone; it opens no more, is listed
// no more and is not revoked twice.
func TestAShareLinkIsKeptHashedAndSealedNeverInPlaintext(t *testing.T) {
	t.Parallel()

	w, _, _ := credentialWorld(t)
	ctx := t.Context()

	chat, err := w.s.CreateChat(ctx, "", "")
	require.NoError(t, err)

	sh := w.shareOf(chat.ID, chat.Bot)
	require.Len(t, sh.Token, 43)

	var (
		token          sql.NullString
		hash, version  string
		inPlainSecrets int
	)
	require.NoError(t, w.db.QueryRowContext(ctx,
		"SELECT token, token_hash, secret_version_id FROM artifact_shares WHERE id = ?", sh.ID).
		Scan(&token, &hash, &version))
	assert.False(t, token.Valid, "no plaintext token")
	assert.Equal(t, bearertoken.Hash(sh.Token), hash)
	assert.NotEmpty(t, version, "a sealed copy")

	require.NoError(t, w.db.QueryRowContext(ctx,
		"SELECT count(*) FROM secret_versions WHERE instr(ciphertext, ?) > 0", sh.Token).Scan(&inPlainSecrets))
	assert.Zero(t, inPlainSecrets, "the vault keeps it sealed")

	shown, err := w.s.SharedArtifact(ctx, sh.Token)
	require.NoError(t, err)
	assert.Equal(t, sh.ID, shown.ShareID)

	listed, err := w.s.ListArtifactShares(ctx, sh.ArtifactID)
	require.NoError(t, err)
	require.Len(t, listed, 1)
	assert.Equal(t, sh.Token, listed[0].Token, "the owner reads the link again")

	all, err := w.s.ListSharedLinks(ctx)
	require.NoError(t, err)
	require.Len(t, all, 1)
	assert.Equal(t, sh.Token, all[0].Token)

	users, err := w.s.SharesUsingSecret(ctx, strings.ReplaceAll(sh.ID, "_", "-"))
	require.NoError(t, err)
	assert.Equal(t, []string{"link " + sh.ID + " (Laura)"}, users, "the vault names it as the secret's user")

	require.NoError(t, w.s.DeleteArtifactShare(ctx, sh.ID))

	assert.Equal(t, string(bearertoken.RevokedByOwner), w.revocation("artifact_shares", sh.ID))

	var secrets int
	require.NoError(t, w.db.QueryRowContext(ctx, "SELECT count(*) FROM secret_versions WHERE id = ?", version).
		Scan(&secrets))
	assert.Zero(t, secrets, "its sealed copy is deleted")

	_, err = w.s.SharedArtifact(ctx, sh.Token)
	require.ErrorIs(t, err, ErrNotFound, "a revoked link opens nothing")

	listed, err = w.s.ListArtifactShares(ctx, sh.ArtifactID)
	require.NoError(t, err)
	assert.Empty(t, listed)

	require.ErrorIs(t, w.s.DeleteArtifactShare(ctx, sh.ID), ErrNotFound)
}

// legacyLinkDB makes a database that holds a link from before tokens were hashed, its token in plaintext, migrated
// since and closed, as `hypomnema migrate` leaves it before the control plane starts. It returns the file, the link,
// and the keyring its vault seals with.
func legacyLinkDB(t *testing.T) (string, ArtifactShare, *vault.Keyring) {
	t.Helper()

	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "hypomnema.db")
	keys := testKeys(t)

	db, err := sqlite.Open(ctx, sqlite.Config{Path: path})
	require.NoError(t, err)
	require.NoError(t, Migrate(ctx, db))

	s := New(db, &fakeClock{now: t0}, platform.NewID).WithShareVault(testVaultWith(db, keys).Copies())

	chat, err := s.CreateChat(ctx, "", "")
	require.NoError(t, err)
	a, _, err := s.ShowArtifact(ctx, ArtifactMaker{Bot: chat.Bot, Chat: chat.ID},
		ShowArtifact{Title: "Sales", Pack: page(t, "<p>sales")})
	require.NoError(t, err)
	sh, err := s.CreateArtifactShare(ctx, a.ID, "Laura")
	require.NoError(t, err)

	// The link as the schema before kept it: the migration's way down keeps a link whose plaintext it still has.
	_, err = db.ExecContext(ctx, "UPDATE artifact_shares SET token = ? WHERE id = ?", sh.Token, sh.ID)
	require.NoError(t, err)

	p, err := provider(db)
	require.NoError(t, err)
	_, err = p.DownTo(ctx, credentialStateBefore(t))
	require.NoError(t, err)

	var old string
	require.NoError(t, db.QueryRowContext(ctx, "SELECT token FROM artifact_shares WHERE id = ?", sh.ID).Scan(&old))
	require.Equal(t, sh.Token, old, "the link from before, in plaintext")

	require.NoError(t, Migrate(ctx, db))
	require.NoError(t, db.Close())

	return path, sh, keys
}

// onDisk reports whether the database's files hold the bytes: the file itself, its write-ahead log and its index.
func onDisk(t *testing.T, path, value string) bool {
	t.Helper()

	for _, name := range []string{path, path + "-wal", path + "-shm"} {
		data, err := os.ReadFile(name) //nolint:gosec // the test's own database
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}

		require.NoError(t, err)

		if bytes.Contains(data, []byte(value)) {
			return true
		}
	}

	return false
}

// openLegacy opens the database legacyLinkDB made, with a Service whose vault seals with keys (none for nil).
func openLegacy(t *testing.T, path string, keys *vault.Keyring) (*sql.DB, *Service) {
	t.Helper()

	db, err := sqlite.Open(t.Context(), sqlite.Config{Path: path})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	return db, New(db, &fakeClock{now: t0}, platform.NewID).WithShareVault(testVaultWith(db, keys).Copies())
}

// Scenario (issue #996): a database holds a link from before tokens were hashed. The control plane starts on it
// after `hypomnema migrate` closed it, moves the link and clears the files: its URL still opens, the owner reads it
// from the vault, and its plaintext is nowhere in the database's files any more, though it was there before (the
// migration's rebuild and the write-ahead log kept it). A second move finds nothing.
func TestALinkFromBeforeStillOpensAfterTheMigrationAndTheMove(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	path, sh, keys := legacyLinkDB(t)
	require.True(t, onDisk(t, path, sh.Token), "the plaintext is in the file before the move")

	db, s := openLegacy(t, path, keys)

	move, err := s.MoveShareTokens(ctx)
	require.NoError(t, err)
	assert.Equal(t, ShareTokenMove{Moved: 1}, move)

	done, err := s.ScrubShareTokens(ctx)
	require.NoError(t, err)
	assert.True(t, done)
	assert.False(t, onDisk(t, path, sh.Token), "no plaintext is left in the files after the move")

	shown, err := s.SharedArtifact(ctx, sh.Token)
	require.NoError(t, err)
	assert.Equal(t, sh.ID, shown.ShareID, "its URL still opens")

	listed, err := s.ListArtifactShares(ctx, sh.ArtifactID)
	require.NoError(t, err)
	require.Len(t, listed, 1)
	assert.Equal(t, sh.Token, listed[0].Token, "read again from the vault")

	move, err = s.MoveShareTokens(ctx)
	require.NoError(t, err)
	assert.Zero(t, move)

	require.NoError(t, db.Close())
	assert.False(t, onDisk(t, path, sh.Token), "nor once it is closed")
}

// Scenario (issue #996, review of #999): the control plane stops after the move's commit and before it clears the
// files. The next start finds nothing to move, but what the files owe is recorded: no link is served until it clears
// them, which it then does.
func TestAStopBetweenTheMoveAndTheClearingIsFinishedAtTheNextStart(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	path, sh, keys := legacyLinkDB(t)

	db, s := openLegacy(t, path, keys)
	move, err := s.MoveShareTokens(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, move.Moved)
	require.NoError(t, db.Close(), "stopped before the clearing")
	require.True(t, onDisk(t, path, sh.Token), "the plaintext is still in the files")

	_, s = openLegacy(t, path, keys)

	move, err = s.MoveShareTokens(ctx)
	require.NoError(t, err)
	assert.Zero(t, move, "nothing is left to move")

	_, err = s.SharedArtifact(ctx, sh.Token)
	require.ErrorIs(t, err, ErrUnavailable, "no link is served while the files owe their clearing")

	done, err := s.ScrubShareTokens(ctx)
	require.NoError(t, err)
	assert.True(t, done)
	assert.False(t, onDisk(t, path, sh.Token))

	_, err = s.SharedArtifact(ctx, sh.Token)
	require.NoError(t, err)
}

// Scenario (issue #996, review of #999): another connection holds a read of an older snapshot while the files are
// cleared. The log cannot be written back, so the clearing is not done and no link is served; once the reader ends,
// the next try clears the files.
func TestAReaderHoldingASnapshotDelaysTheClearing(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	path, sh, keys := legacyLinkDB(t)

	_, s := openLegacy(t, path, keys)
	move, err := s.MoveShareTokens(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, move.Moved)

	other, err := sqlite.Open(ctx, sqlite.Config{Path: path})
	require.NoError(t, err)
	t.Cleanup(func() { _ = other.Close() })

	reader, err := other.Conn(ctx)
	require.NoError(t, err)
	_, err = reader.ExecContext(ctx, "BEGIN DEFERRED")
	require.NoError(t, err)

	var n int
	require.NoError(t, reader.QueryRowContext(ctx, "SELECT count(*) FROM artifact_shares").Scan(&n))

	done, _ := s.ScrubShareTokens(ctx)
	assert.False(t, done, "the checkpoint is busy")

	_, err = s.SharedArtifact(ctx, sh.Token)
	require.ErrorIs(t, err, ErrUnavailable)

	_, err = reader.ExecContext(ctx, "COMMIT")
	require.NoError(t, err)
	require.NoError(t, reader.Close())

	done, err = s.ScrubShareTokens(ctx)
	require.NoError(t, err)
	assert.True(t, done)
	assert.False(t, onDisk(t, path, sh.Token))

	_, err = s.SharedArtifact(ctx, sh.Token)
	require.NoError(t, err)
}

// Scenario (issue #996): the control plane starts without its vault's keys on a database with a link from before.
// The link is not served and its plaintext is not read back to the owner: it is kept only until a vault seals it,
// since without a sealed copy he could never read it again; the move says it waits. A new link is refused rather
// than kept by its hash alone. Once the keys are back, the next start moves it and clears the files.
func TestWithoutTheVaultALinkFromBeforeWaitsAndNoLinkIsMade(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	path, sh, keys := legacyLinkDB(t)

	db, s := openLegacy(t, path, nil)

	move, err := s.MoveShareTokens(ctx)
	require.NoError(t, err)
	assert.Equal(t, ShareTokenMove{Waiting: 1}, move)

	done, err := s.ScrubShareTokens(ctx)
	require.NoError(t, err)
	assert.False(t, done, "nothing is cleared while a token waits")

	var plain sql.NullString
	require.NoError(t, db.QueryRowContext(ctx, "SELECT token FROM artifact_shares WHERE id = ?", sh.ID).Scan(&plain))
	assert.Equal(t, sh.Token, plain.String, "kept until it can be sealed")

	_, err = s.SharedArtifact(ctx, sh.Token)
	require.ErrorIs(t, err, ErrUnavailable, "not served from its plaintext")

	listed, err := s.ListArtifactShares(ctx, sh.ArtifactID)
	require.NoError(t, err)
	require.Len(t, listed, 1)
	assert.Empty(t, listed[0].Token, "nor read back from it")

	_, err = s.CreateArtifactShare(ctx, sh.ArtifactID, "Huy")
	require.ErrorIs(t, err, ErrUnavailable, "no link is kept by its hash alone")

	all, err := s.ListSharedLinks(ctx)
	require.NoError(t, err)
	assert.Len(t, all, 1, "the refused link was not made")

	s = New(db, &fakeClock{now: t0}, platform.NewID).WithShareVault(testVaultWith(db, keys).Copies())

	move, err = s.MoveShareTokens(ctx)
	require.NoError(t, err)
	assert.Equal(t, ShareTokenMove{Moved: 1}, move)

	done, err = s.ScrubShareTokens(ctx)
	require.NoError(t, err)
	assert.True(t, done)

	_, err = s.SharedArtifact(ctx, sh.Token)
	require.NoError(t, err, "served once sealed")

	listed, err = s.ListArtifactShares(ctx, sh.ArtifactID)
	require.NoError(t, err)
	require.Len(t, listed, 1)
	assert.Equal(t, sh.Token, listed[0].Token, "read from the vault now")
}

// credentialStateBefore returns the version of the migration before this one, which `make migration-fix` renumbers
// before the merge.
func credentialStateBefore(t *testing.T) int64 {
	t.Helper()

	entries, err := fs.ReadDir(migrations, "migrations")
	require.NoError(t, err)

	var before int64

	for _, e := range entries {
		version, err := strconv.ParseInt(strings.SplitN(e.Name(), "_", 2)[0], 10, 64)
		require.NoError(t, err)

		if strings.HasSuffix(e.Name(), "_credential_state.sql") {
			return before
		}

		before = version
	}

	require.FailNow(t, "no credential_state migration")

	return 0
}

// Scenario (issue #996, rule 5 of #995): the owner revokes a coordinator token. Its children end with it, each with
// the reason parent_revoked, and are refused from then on: the gateway key of a chat whose pi ran under it, the agent
// session and gateway key of a Dispatch of a Run it created, and the Bot token made under it. Another coordinator's
// chat key and a Dispatch of the owner's Run keep working.
func TestRevokingACoordinatorTokenEndsItsChildren(t *testing.T) {
	t.Parallel()

	w, g, _ := credentialWorld(t)
	ctx := t.Context()

	coordinator, _, err := w.s.AddToken(ctx, "pi", bearertoken.Coordinator)
	require.NoError(t, err)
	other, _, err := w.s.AddToken(ctx, "other", bearertoken.Coordinator)
	require.NoError(t, err)

	chat, err := w.s.CreateChat(ctx, "", "")
	require.NoError(t, err)
	chatKey, chatSecret, err := g.AddChatKeyUnder(ctx, coordinator.ID, chat.ID, chat.Bot, "")
	require.NoError(t, err)
	otherChat, err := w.s.CreateChat(ctx, "", "")
	require.NoError(t, err)
	_, otherChatSecret, err := g.AddChatKeyUnder(ctx, other.ID, otherChat.ID, otherChat.Bot, "")
	require.NoError(t, err)
	botSecret, err := w.s.AddBotToken(ctx, "pi", chat.ID, chat.Bot)
	require.NoError(t, err)

	_, err = w.db.ExecContext(ctx, "UPDATE runs SET coordinator_token_id = ? WHERE id = ?", coordinator.ID, w.run.ID)
	require.NoError(t, err)
	theirs := w.dispatch(w.tasks(NewTask{Ref: "a", Profile: "implementer", Spec: "a"})[0].ID)

	owners := newRun(t, w.s)
	ownerTasks, err := w.s.CreateTasks(ctx, owners.ID, []NewTask{{Ref: "b", Profile: "implementer", Spec: "b"}})
	require.NoError(t, err)
	mine := w.dispatch(ownerTasks[0].ID)

	theirSession, err := w.s.AddAgentSession(ctx, theirs.ID, nil)
	require.NoError(t, err)
	theirKey, err := g.AddDispatchKey(ctx, theirs.ID, "model-a")
	require.NoError(t, err)
	mySession, err := w.s.AddAgentSession(ctx, mine.ID, nil)
	require.NoError(t, err)
	myKey, err := g.AddDispatchKey(ctx, mine.ID, "model-a")
	require.NoError(t, err)

	require.NoError(t, w.s.RevokeToken(ctx, coordinator.ID))

	revoked, err := w.s.GetToken(ctx, coordinator.ID)
	require.NoError(t, err)
	assert.Equal(t, bearertoken.RevokedByOwner, revoked.RevokeReason)

	_, err = g.Authenticate(ctx, chatSecret)
	require.ErrorIs(t, err, modelgateway.ErrUnauthorized, "its chat's key")
	assert.Equal(t, string(bearertoken.RevokedParentRevoked), w.revocation("gateway_keys", chatKey.ID))
	_, err = g.Authenticate(ctx, theirKey)
	require.ErrorIs(t, err, modelgateway.ErrUnauthorized, "its Run's Dispatch key")
	_, err = w.s.AgentSessionOf(ctx, theirSession)
	require.ErrorIs(t, err, ErrNotFound, "its Run's agent session")

	var sessionReason string
	require.NoError(t, w.db.QueryRowContext(ctx,
		"SELECT revoke_reason FROM agent_sessions WHERE dispatch_id = ?", theirs.ID).Scan(&sessionReason))
	assert.Equal(t, string(bearertoken.RevokedParentRevoked), sessionReason)

	_, err = w.s.AuthenticateToken(ctx, botSecret)
	require.ErrorIs(t, err, ErrNotFound, "its Bot token")
	assert.Empty(t, w.s.botTokens.byHash, "and the Bot token is forgotten")

	_, err = g.Authenticate(ctx, otherChatSecret)
	require.NoError(t, err, "another coordinator's chat key works")
	_, err = g.Authenticate(ctx, myKey)
	require.NoError(t, err, "the owner's Run's key works")
	_, err = w.s.AgentSessionOf(ctx, mySession)
	require.NoError(t, err)
}

// Scenario (issue #996): the owner removes a node whose Dispatch settled before the keys sweep ended its keys. The
// node's tokens and the Dispatch's agent session and gateway key end with the reason node_removed and are refused.
func TestRemovingANodeEndsTheKeysOfItsDispatches(t *testing.T) {
	t.Parallel()

	w, g, _ := credentialWorld(t)
	ctx := t.Context()

	n := w.node
	_, nodeToken, err := w.s.AddNodeToken(ctx, n.ID, "second")
	require.NoError(t, err)

	task := w.tasks(NewTask{Ref: "a", Profile: "implementer", Spec: "a"})[0]
	d, c := w.start(task.ID)
	session, err := w.s.AddAgentSession(ctx, d.ID, nil)
	require.NoError(t, err)
	key, err := g.AddDispatchKey(ctx, d.ID, "model-a")
	require.NoError(t, err)
	require.NoError(t, w.finish(c, success("done")))

	require.NoError(t, w.s.RemoveNode(ctx, n.ID))

	_, _, err = w.s.AuthenticateNodeToken(ctx, nodeToken)
	require.ErrorIs(t, err, ErrNotFound)

	tokens, err := w.s.ListNodeTokens(ctx, n.ID)
	require.NoError(t, err)
	require.Len(t, tokens, 2)

	for _, tok := range tokens {
		assert.Equal(t, bearertoken.RevokedNodeRemoved, tok.RevokeReason)
	}

	_, err = w.s.AgentSessionOf(ctx, session)
	require.ErrorIs(t, err, ErrNotFound)
	_, err = g.Authenticate(ctx, key)
	require.ErrorIs(t, err, modelgateway.ErrUnauthorized)

	var sessionReason, keyReason string
	require.NoError(t, w.db.QueryRowContext(ctx,
		"SELECT revoke_reason FROM agent_sessions WHERE dispatch_id = ?", d.ID).Scan(&sessionReason))
	require.NoError(t, w.db.QueryRowContext(ctx,
		"SELECT revoke_reason FROM gateway_keys WHERE dispatch_id = ?", d.ID).Scan(&keyReason))
	assert.Equal(t, string(bearertoken.RevokedNodeRemoved), sessionReason)
	assert.Equal(t, string(bearertoken.RevokedNodeRemoved), keyReason)
}

// Scenario (issue #996): the owner deletes a chat that has a link, a pi's key and a pi's Bot token. The key ends
// with the reason chat_deleted, the link and its sealed copy go with the chat's artifact, and the Bot token is
// refused. Deleting a Bot ends its keys with the reason bot_deleted and its pi's Bot tokens.
func TestDeletingAChatOrABotEndsItsCredentials(t *testing.T) {
	t.Parallel()

	w, g, _ := credentialWorld(t)
	ctx := t.Context()

	_, _, err := w.s.AddToken(ctx, "pi", bearertoken.Coordinator)
	require.NoError(t, err)

	chat, err := w.s.CreateChat(ctx, "", "")
	require.NoError(t, err)
	sh := w.shareOf(chat.ID, chat.Bot)
	key, keySecret, err := g.AddChatKey(ctx, chat.ID, chat.Bot, "")
	require.NoError(t, err)
	botSecret, err := w.s.AddBotToken(ctx, "pi", chat.ID, chat.Bot)
	require.NoError(t, err)

	var version string
	require.NoError(t, w.db.QueryRowContext(ctx,
		"SELECT secret_version_id FROM artifact_shares WHERE id = ?", sh.ID).Scan(&version))

	_, err = w.s.DeleteChat(ctx, chat.ID)
	require.NoError(t, err)

	_, err = g.Authenticate(ctx, keySecret)
	require.ErrorIs(t, err, modelgateway.ErrUnauthorized)
	assert.Equal(t, string(bearertoken.RevokedChatDeleted), w.revocation("gateway_keys", key.ID))
	_, err = w.s.SharedArtifact(ctx, sh.Token)
	require.ErrorIs(t, err, ErrNotFound)

	var secrets int
	require.NoError(t, w.db.QueryRowContext(ctx, "SELECT count(*) FROM secret_versions WHERE id = ?", version).
		Scan(&secrets))
	assert.Zero(t, secrets, "the link's sealed copy goes with it")

	_, err = w.s.AuthenticateToken(ctx, botSecret)
	require.ErrorIs(t, err, ErrNotFound, "the chat's Bot token")

	helper, err := w.s.PutBot(ctx, Bot{Name: "helper"})
	require.NoError(t, err)
	helperChat, err := w.s.CreateChat(ctx, helper.ID, "")
	require.NoError(t, err)
	helperSecret, err := w.s.AddBotToken(ctx, "pi", helperChat.ID, helper.ID)
	require.NoError(t, err)
	// A key of the Bot that no chat holds any more, as a deleted chat's keys are kept (issue #405).
	_, err = w.db.ExecContext(ctx, `INSERT INTO gateway_keys (id, name, key_hash, models, created_at, bot_id)
		VALUES ('gwk_bot', 'bot-key', 'hash-bot', '["*"]', 1, ?)`, helper.ID)
	require.NoError(t, err)

	_, err = w.s.ForceDeleteBot(ctx, helper.ID)
	require.NoError(t, err)

	assert.Equal(t, string(bearertoken.RevokedBotDeleted), w.revocation("gateway_keys", "gwk_bot"))
	_, err = w.s.AuthenticateToken(ctx, helperSecret)
	require.ErrorIs(t, err, ErrNotFound, "the Bot's pi token")
}

// Scenario (issue #996): a client token, an agent session and a gateway key in use record their last use at most
// once an hour, as node tokens do: the first use writes it, a use within the hour does not, one after it does.
func TestLastUseIsWrittenAtMostHourly(t *testing.T) {
	t.Parallel()

	w, g, _ := credentialWorld(t)
	ctx := t.Context()

	tok, secret, err := w.s.AddToken(ctx, "laptop", bearertoken.Owner)
	require.NoError(t, err)
	task := w.tasks(NewTask{Ref: "a", Profile: "implementer", Spec: "a"})[0]
	d := w.dispatch(task.ID)
	session, err := w.s.AddAgentSession(ctx, d.ID, nil)
	require.NoError(t, err)
	keySecret, err := g.AddDispatchKey(ctx, d.ID, "model-a")
	require.NoError(t, err)

	use := func() {
		t.Helper()

		_, err := w.s.AuthenticateToken(ctx, secret)
		require.NoError(t, err)
		_, err = w.s.AgentSessionOf(ctx, session)
		require.NoError(t, err)
		_, err = g.Authenticate(ctx, keySecret)
		require.NoError(t, err)
	}

	lastUsed := func() map[string]int64 {
		t.Helper()

		out := map[string]int64{}
		for table, q := range map[string]string{
			"client_tokens":  "SELECT last_used_at FROM client_tokens WHERE id = '" + tok.ID + "'",
			"agent_sessions": "SELECT last_used_at FROM agent_sessions WHERE dispatch_id = '" + d.ID + "'",
			"gateway_keys":   "SELECT last_used_at FROM gateway_keys WHERE dispatch_id = '" + d.ID + "'",
		} {
			var at sql.NullInt64
			require.NoError(t, w.db.QueryRowContext(ctx, q).Scan(&at))
			out[table] = at.Int64
		}

		return out
	}

	first := w.clock.now
	use()

	for table, at := range lastUsed() {
		assert.Equal(t, first.UnixMilli(), at, table)
	}

	w.advance(59 * time.Minute)
	use()

	for table, at := range lastUsed() {
		assert.Equal(t, first.UnixMilli(), at, "%s: not again within the hour", table)
	}

	w.advance(2 * time.Minute)
	use()

	for table, at := range lastUsed() {
		assert.Equal(t, w.clock.now.UnixMilli(), at, "%s: again after the hour", table)
	}

	got, err := w.s.GetToken(ctx, tok.ID)
	require.NoError(t, err)
	require.NotNil(t, got.LastUsedAt)
	assert.Equal(t, w.clock.now, *got.LastUsedAt)
}

var _ GatewayKeys = modelgateway.Revocations{}

var _ ShareVault = vault.Copies{}

// Scenario (issue #996, review of #999): a node is told an offer, read while the Run's coordinator token works, and
// its keys are made after the owner revoked that token, as an offer sent again after the node reconnects is. Neither
// the agent session nor the gateway key is made. A Dispatch of a removed node gets none either.
func TestNoKeyIsMadeForADispatchWhoseParentEnded(t *testing.T) {
	t.Parallel()

	t.Run("its coordinator token revoked", func(t *testing.T) {
		t.Parallel()

		w, g, _ := credentialWorld(t)
		ctx := t.Context()

		coordinator, _, err := w.s.AddToken(ctx, "pi", bearertoken.Coordinator)
		require.NoError(t, err)
		_, err = w.db.ExecContext(ctx, "UPDATE runs SET coordinator_token_id = ? WHERE id = ?", coordinator.ID,
			w.run.ID)
		require.NoError(t, err)
		d := w.dispatch(w.tasks(NewTask{Ref: "a", Profile: "implementer", Spec: "a"})[0].ID)

		pending, err := w.s.NodePending(ctx, w.node.ID)
		require.NoError(t, err)
		require.Len(t, pending.Offers, 1, "the offer is read before the revocation")

		require.NoError(t, w.s.RevokeToken(ctx, coordinator.ID))

		_, err = w.s.AddAgentSession(ctx, d.ID, nil)
		require.ErrorIs(t, err, ErrConflict)
		_, err = g.AddDispatchKey(ctx, d.ID, "model-a")
		require.ErrorIs(t, err, modelgateway.ErrConflict)

		var sessions, keys int
		require.NoError(t, w.db.QueryRowContext(ctx, "SELECT count(*) FROM agent_sessions WHERE dispatch_id = ?",
			d.ID).Scan(&sessions))
		require.NoError(t, w.db.QueryRowContext(ctx, "SELECT count(*) FROM gateway_keys WHERE dispatch_id = ?",
			d.ID).Scan(&keys))
		assert.Zero(t, sessions)
		assert.Zero(t, keys)
	})

	t.Run("its node removed", func(t *testing.T) {
		t.Parallel()

		w, g, _ := credentialWorld(t)
		ctx := t.Context()

		d, c := w.start(w.tasks(NewTask{Ref: "a", Profile: "implementer", Spec: "a"})[0].ID)
		require.NoError(t, w.finish(c, success("done")))
		require.NoError(t, w.s.RemoveNode(ctx, w.node.ID))

		_, err := w.s.AddAgentSession(ctx, d.ID, nil)
		require.ErrorIs(t, err, ErrConflict)
		_, err = g.AddDispatchKey(ctx, d.ID, "model-a")
		require.ErrorIs(t, err, modelgateway.ErrConflict)
	})
}

// Scenario (issue #996, review of #999): a pi's Bot token is in memory after its chat, or its Bot, was deleted: a
// request between the deletion's commit and its clean-up, or a token made from a read of the chat before the
// deletion and kept after the clean-up. It is refused, and forgotten.
func TestABotTokenEndsWithItsChatOrBotAtOnce(t *testing.T) {
	t.Parallel()

	w, _, _ := credentialWorld(t)
	ctx := t.Context()

	coordinator, _, err := w.s.AddToken(ctx, "pi", bearertoken.Coordinator)
	require.NoError(t, err)

	chat, err := w.s.CreateChat(ctx, "", "")
	require.NoError(t, err)
	live, err := w.s.AddBotToken(ctx, "pi", chat.ID, chat.Bot)
	require.NoError(t, err)
	_, err = w.s.AuthenticateToken(ctx, live)
	require.NoError(t, err)

	_, err = w.s.DeleteChat(ctx, chat.ID)
	require.NoError(t, err)

	stale, err := w.s.addBotToken(botToken{coordinatorID: coordinator.ID, chat: chat.ID, bot: chat.Bot})
	require.NoError(t, err)
	_, err = w.s.AuthenticateToken(ctx, stale)
	require.ErrorIs(t, err, ErrNotFound, "a deleted chat's token")

	helper, err := w.s.PutBot(ctx, Bot{Name: "helper"})
	require.NoError(t, err)
	require.NoError(t, w.s.DeleteBot(ctx, helper.ID))

	staleBot, err := w.s.addBotToken(botToken{coordinatorID: coordinator.ID, bot: helper.ID, routine: "rtn_1"})
	require.NoError(t, err)
	_, err = w.s.AuthenticateToken(ctx, staleBot)
	require.ErrorIs(t, err, ErrNotFound, "a deleted Bot's token")

	assert.Empty(t, w.s.botTokens.byHash, "refused tokens are forgotten")
}

// Scenario (issue #996, review of #999): a link is opened again and again. Each opening is counted, and its last
// use is written at most once an hour; a revoked link's is not written.
func TestAShareLinksLastViewIsWrittenAtMostHourly(t *testing.T) {
	t.Parallel()

	w, _, _ := credentialWorld(t)
	ctx := t.Context()

	chat, err := w.s.CreateChat(ctx, "", "")
	require.NoError(t, err)
	sh := w.shareOf(chat.ID, chat.Bot)

	read := func() (int64, int64) {
		t.Helper()

		var views int64
		var last sql.NullInt64
		require.NoError(t, w.db.QueryRowContext(ctx, "SELECT views, last_viewed_at FROM artifact_shares WHERE id = ?",
			sh.ID).Scan(&views, &last))

		return views, last.Int64
	}

	first := w.clock.now
	require.NoError(t, w.s.CountArtifactShareView(ctx, sh.ID))
	w.advance(59 * time.Minute)
	require.NoError(t, w.s.CountArtifactShareView(ctx, sh.ID))

	views, last := read()
	assert.Equal(t, int64(2), views)
	assert.Equal(t, first.UnixMilli(), last, "not written again within the hour")

	w.advance(2 * time.Minute)
	require.NoError(t, w.s.CountArtifactShareView(ctx, sh.ID))

	views, last = read()
	assert.Equal(t, int64(3), views)
	assert.Equal(t, w.clock.now.UnixMilli(), last, "written after the hour")

	require.NoError(t, w.s.DeleteArtifactShare(ctx, sh.ID))
	w.advance(2 * time.Hour)
	require.NoError(t, w.s.CountArtifactShareView(ctx, sh.ID))

	views, revokedLast := read()
	assert.Equal(t, int64(3), views, "a revoked link is not counted")
	assert.Equal(t, last, revokedLast)
}

// Scenario (issue #996, review of #999): the owner removes a provider's machine. While it is being destroyed, and
// after a failed try, no node token or Dispatch key is issued for it; keys that are still there when the provider
// says it is gone stop working then, not at the next keys sweep.
func TestAMachineBeingRemovedGetsNoCredentialAndItsKeysEndWhenItIsGone(t *testing.T) {
	t.Parallel()

	w, g, _ := credentialWorld(t)
	ctx := t.Context()

	provider, err := w.s.AddMachineProvider(ctx, NewMachineProvider{
		Name: "aws", Kind: MachineProviderAWS, KeyRef: "vault:aws", Region: "ap-southeast-1", Size: "t3.large",
	})
	require.NoError(t, err)

	d, c := w.start(w.tasks(NewTask{Ref: "a", Profile: "implementer", Spec: "a"})[0].ID)
	require.NoError(t, w.finish(c, success("done")))

	// The world's node as a provider's machine.
	_, err = w.db.ExecContext(ctx, "UPDATE nodes SET provider_id = ?, machine_state = 'ready' WHERE id = ?",
		provider.ID, w.node.ID)
	require.NoError(t, err)
	require.NoError(t, w.s.BeginMachineRemoval(ctx, w.node.ID))

	refused := func(state string) {
		t.Helper()

		_, _, err := w.s.AddNodeToken(ctx, w.node.ID, "late")
		require.ErrorIs(t, err, ErrNotFound, "a node token while %s", state)
		_, err = w.s.AddAgentSession(ctx, d.ID, nil)
		require.ErrorIs(t, err, ErrConflict, "an agent session while %s", state)
		_, err = g.AddDispatchKey(ctx, d.ID, "model-a")
		require.ErrorIs(t, err, modelgateway.ErrConflict, "a gateway key while %s", state)
	}

	refused("deleting")

	require.NoError(t, w.s.MachineRemovalFailed(ctx, w.node.ID, "the provider said no", w.clock.now.Add(time.Hour)))
	refused("delete_failed")

	// Keys the machine still has, as made before their issuing checked it.
	session, err := bearertoken.Generate(bearertoken.Session)
	require.NoError(t, err)
	key, err := bearertoken.Generate(bearertoken.Gateway)
	require.NoError(t, err)
	_, err = w.db.ExecContext(ctx, `INSERT INTO agent_sessions (id, key_hash, dispatch_id, uses, created_at)
		VALUES ('ags_left', ?, ?, '', 1)`, bearertoken.Hash(session), d.ID)
	require.NoError(t, err)
	_, err = w.db.ExecContext(ctx, `INSERT INTO gateway_keys (id, name, key_hash, models, created_at, dispatch_id)
		VALUES ('gwk_left', 'left', ?, '["*"]', 1, ?)`, bearertoken.Hash(key), d.ID)
	require.NoError(t, err)

	require.NoError(t, w.s.MachineRemoved(ctx, w.node.ID))

	_, err = w.s.AgentSessionOf(ctx, session)
	require.ErrorIs(t, err, ErrNotFound)
	_, err = g.Authenticate(ctx, key)
	require.ErrorIs(t, err, modelgateway.ErrUnauthorized)
	assert.Equal(t, string(bearertoken.RevokedNodeRemoved), w.revocation("gateway_keys", "gwk_left"))
}

// Scenario (issue #996, review of #999): reading a Bot token's chat fails for a moment (its table cannot be read).
// The token is refused for that request but not forgotten: once the database answers, it works again.
func TestABotTokenOutlivesAFailedRead(t *testing.T) {
	t.Parallel()

	w, _, _ := credentialWorld(t)
	ctx := t.Context()

	_, _, err := w.s.AddToken(ctx, "pi", bearertoken.Coordinator)
	require.NoError(t, err)
	chat, err := w.s.CreateChat(ctx, "", "")
	require.NoError(t, err)
	secret, err := w.s.AddBotToken(ctx, "pi", chat.ID, chat.Bot)
	require.NoError(t, err)

	_, err = w.db.ExecContext(ctx, "ALTER TABLE chats RENAME TO chats_away")
	require.NoError(t, err)

	_, err = w.s.AuthenticateToken(ctx, secret)
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrNotFound, "a failed read says nothing of the binding")

	_, err = w.db.ExecContext(ctx, "ALTER TABLE chats_away RENAME TO chats")
	require.NoError(t, err)

	got, err := w.s.AuthenticateToken(ctx, secret)
	require.NoError(t, err, "the token was kept")
	assert.Equal(t, chat.ID, got.Chat)
}

// Scenario (issue #996, review of #999): a link is opened again and again within a minute. The openings are not
// written one by one: the first is written at once, the next ones together after a minute, and the lists count
// them all meanwhile.
func TestAShareLinksViewsAreWrittenTogether(t *testing.T) {
	t.Parallel()

	w, _, _ := credentialWorld(t)
	ctx := t.Context()

	chat, err := w.s.CreateChat(ctx, "", "")
	require.NoError(t, err)
	sh := w.shareOf(chat.ID, chat.Bot)

	stored := func() int64 {
		t.Helper()

		var views int64
		require.NoError(t, w.db.QueryRowContext(ctx, "SELECT views FROM artifact_shares WHERE id = ?", sh.ID).
			Scan(&views))

		return views
	}

	for range 5 {
		require.NoError(t, w.s.CountArtifactShareView(ctx, sh.ID))
		w.advance(time.Second)
	}

	assert.Equal(t, int64(1), stored(), "one write for five openings within the minute")

	listed, err := w.s.ListArtifactShares(ctx, sh.ArtifactID)
	require.NoError(t, err)
	require.Len(t, listed, 1)
	assert.Equal(t, int64(5), listed[0].Views, "the list counts the openings not written yet")

	w.advance(time.Minute)
	require.NoError(t, w.s.CountArtifactShareView(ctx, sh.ID))
	assert.Equal(t, int64(6), stored(), "written together after the minute")
}
