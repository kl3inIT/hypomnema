package telegram

import (
	"encoding/hex"
	"testing"
	"time"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"github.com/gotd/td/tgmock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/messaging"
)

// The linking of the owner's account (issue #1184), each step against a fake Telegram (gotd's tgmock, answering the
// requests gotd's qrlogin and auth make), as gotd tests its own QR login (telegram/auth/qrlogin/password_test.go).
// No real account, number, code or password is used.

const (
	testAppID   = 12345
	testAppHash = "0123456789abcdef0123456789abcdef"
)

// testPassword is what account.getPassword answers for an account with a two-step password: gotd's own test
// parameters (telegram/auth/qrlogin/password_test.go, testPassword; MIT), with the owner's hint.
func testPassword(t *testing.T) *tg.AccountPassword {
	t.Helper()

	hexDecode := func(s string) []byte {
		b, err := hex.DecodeString(s)
		require.NoError(t, err)

		return b
	}
	algo := &tg.PasswordKdfAlgoSHA256SHA256PBKDF2HMACSHA512iter100000SHA256ModPow{
		Salt1: hexDecode("4D11FB6BEC38F9D2546BB0F61E4F1C99A1BC0DB8F0D5F35B1291B37B213123D7ED48F3C6794D495B"),
		Salt2: hexDecode("A1B181AAFE88188680AE32860D60BB01"),
		G:     3,
		P: hexDecode("C71CAEB9C6B1C9048E6C522F70F13F73980D40238E3E21C14934D037563D930F" +
			"48198A0AA7C14058229493D22530F4DBFA336F6E0AC925139543AED44CCE7C37" +
			"20FD51F69458705AC68CD4FE6B6B13ABDC9746512969328454F18FAF8C595F64" +
			"2477FE96BB2A941D5BCD1D4AC8CC49880708FA9B378E3C4F3A9060BEE67CF9A4" +
			"A4A695811051907E162753B56B0F6B410DBA74D8A84B2A14B3144E0EF1284754" +
			"FD17ED950D5965B4B9DD46582DB1178D169C6BC465B0D6FF9CA3928FEF5B9AE4" +
			"E418FC15E83EBEA0F87FA9FF5EED70050DED2849F47BF959D956850CE929851F" +
			"0D8115F635B105EE2E4E15D04B2454BF6F4FADF034B10403119CD8E3B92FCC5B"),
	}
	pwd := &tg.AccountPassword{
		HasPassword: true, Hint: "the cat", NewAlgo: algo,
		NewSecureAlgo: &tg.SecurePasswordKdfAlgoPBKDF2HMACSHA512iter100000{}, SRPID: 1,
	}
	pwd.SetCurrentAlgo(algo)

	return pwd
}

// linking is one linking against a fake Telegram: its steps as the page reads them, and the phone that accepts a QR
// code.
type linking struct {
	mock     *tgmock.Mock
	steps    chan messaging.LinkStep
	loggedIn chan struct{}
	done     chan linked
}

type linked struct {
	authz *tg.AuthAuthorization
	err   error
}

func startLinking(t *testing.T, byPhone bool, expect func(m *tgmock.Mock)) *linking {
	t.Helper()

	l := &linking{
		mock: tgmock.NewRequire(t), steps: make(chan messaging.LinkStep, 4), loggedIn: make(chan struct{}, 1),
		done: make(chan linked, 1),
	}
	expect(l.mock)

	lk := linker{
		api: tg.NewClient(l.mock), appID: testAppID, appHash: testAppHash, loggedIn: l.loggedIn, steps: l.steps,
		answers: make(chan string),
	}

	go func() {
		run := lk.qr
		if byPhone {
			run = lk.phone
		}

		authz, err := run(t.Context())
		l.done <- linked{authz, err}
	}()

	return l
}

// next is the linking's next step.
func (l *linking) next(t *testing.T) messaging.LinkStep {
	t.Helper()

	select {
	case step := <-l.steps:
		return step
	case <-time.After(5 * time.Second):
		t.Fatal("the linking showed no next step")

		return messaging.LinkStep{}
	}
}

// answer types an answer to the question the next step asks, which must be field.
func (l *linking) answer(t *testing.T, field, value string) messaging.Ask {
	t.Helper()

	step := l.next(t)
	require.NotNil(t, step.Ask, "the step asks something")
	require.Equal(t, field, step.Ask.Field)
	require.NoError(t, step.Ask.Answer(t.Context(), value))

	return *step.Ask
}

func (l *linking) end(t *testing.T) linked {
	t.Helper()

	select {
	case r := <-l.done:
		return r
	case <-time.After(5 * time.Second):
		t.Fatal("the linking did not end")

		return linked{}
	}
}

var (
	exportQR = &tg.AuthExportLoginTokenRequest{APIID: testAppID, APIHash: testAppHash}
	owner    = &tg.AuthAuthorization{User: &tg.User{ID: self, Phone: "84912345678", FirstName: "Đạt"}}
)

func qrToken() *tg.AuthLoginToken {
	return &tg.AuthLoginToken{Token: []byte("token"), Expires: int(time.Now().Add(time.Minute).Unix())}
}

// checkPassword expects auth.checkPassword with an SRP answer, and answers it.
func checkPassword(t *testing.T, m *tgmock.Mock) *tgmock.RequestBuilder {
	t.Helper()

	return m.ExpectFunc(func(b bin.Encoder) {
		req, ok := b.(*tg.AuthCheckPasswordRequest)
		require.True(t, ok, "%T", b)
		_, ok = req.Password.(*tg.InputCheckPasswordSRP)
		require.True(t, ok)
	})
}

// Scenario: the QR code first. The page shows Telegram's login link as the code; the owner's phone accepts it, and an
// account with no two-step password is linked at once.
func TestALinkByQRShowsTheCodeUntilThePhoneAcceptsIt(t *testing.T) {
	t.Parallel()

	l := startLinking(t, false, func(m *tgmock.Mock) {
		m.ExpectCall(exportQR).ThenResult(qrToken())
		m.ExpectCall(exportQR).ThenResult(&tg.AuthLoginTokenSuccess{Authorization: owner})
	})

	step := l.next(t)
	assert.Equal(t, "tg://login?token=dG9rZW4=", step.Code, "the login link, base64url")
	assert.False(t, step.Expires.IsZero())
	assert.Nil(t, step.Ask)

	l.loggedIn <- struct{}{}
	r := l.end(t)
	require.NoError(t, r.err)
	assert.Equal(t, owner, r.authz)
}

// Scenario: the account has a two-step password. Once the phone accepted the code, the linking asks for it with the
// owner's hint; a wrong one is asked again, and the right one links the account.
func TestALinkByQRAsksThePasswordAgainWhenWrong(t *testing.T) {
	t.Parallel()

	l := startLinking(t, false, func(m *tgmock.Mock) {
		m.ExpectCall(exportQR).ThenResult(qrToken())
		m.ExpectCall(exportQR).ThenRPCErr(tgerr.New(401, "SESSION_PASSWORD_NEEDED"))
		m.ExpectCall(&tg.AccountGetPasswordRequest{}).ThenResult(testPassword(t))
		m.ExpectCall(&tg.AccountGetPasswordRequest{}).ThenResult(testPassword(t))
		checkPassword(t, m).ThenRPCErr(tgerr.New(400, "PASSWORD_HASH_INVALID"))
		m.ExpectCall(&tg.AccountGetPasswordRequest{}).ThenResult(testPassword(t))
		m.ExpectCall(&tg.AccountGetPasswordRequest{}).ThenResult(testPassword(t))
		checkPassword(t, m).ThenResult(owner)
	})

	require.NotEmpty(t, l.next(t).Code)
	l.loggedIn <- struct{}{}

	asked := l.answer(t, messaging.AskPassword, "wrong")
	assert.Equal(t, "the cat", asked.Hint)
	assert.False(t, asked.Again)

	asked = l.answer(t, messaging.AskPassword, "hunter2")
	assert.True(t, asked.Again, "a wrong password is asked again")

	r := l.end(t)
	require.NoError(t, r.err)
	assert.Equal(t, owner, r.authz)
}

// Scenario: the QR code cannot be scanned, so the owner links by his phone number: the linking asks the number, then
// the code Telegram sent to his app; a wrong code is asked again; the account has a two-step password, asked last.
func TestALinkByPhoneAsksTheNumberTheCodeAndThePassword(t *testing.T) {
	t.Parallel()

	sent := &tg.AuthSentCode{Type: &tg.AuthSentCodeTypeApp{Length: 5}, PhoneCodeHash: "hash"}
	signIn := func(code string) *tg.AuthSignInRequest {
		return &tg.AuthSignInRequest{PhoneNumber: "+84912345678", PhoneCodeHash: "hash", PhoneCode: code}
	}

	l := startLinking(t, true, func(m *tgmock.Mock) {
		m.ExpectCall(&tg.AuthSendCodeRequest{PhoneNumber: "84", APIID: testAppID, APIHash: testAppHash}).
			ThenRPCErr(tgerr.New(400, "PHONE_NUMBER_INVALID"))
		m.ExpectCall(&tg.AuthSendCodeRequest{PhoneNumber: "+84912345678", APIID: testAppID, APIHash: testAppHash}).
			ThenResult(sent)
		m.ExpectCall(signIn("11111")).ThenRPCErr(tgerr.New(400, "PHONE_CODE_INVALID"))
		m.ExpectCall(signIn("22222")).ThenRPCErr(tgerr.New(401, "SESSION_PASSWORD_NEEDED"))
		m.ExpectCall(&tg.AccountGetPasswordRequest{}).ThenResult(testPassword(t))
		m.ExpectCall(&tg.AccountGetPasswordRequest{}).ThenResult(testPassword(t))
		checkPassword(t, m).ThenResult(owner)
	})

	assert.False(t, l.answer(t, messaging.AskPhone, "84").Again)
	assert.True(t, l.answer(t, messaging.AskPhone, "+84912345678").Again, "a number Telegram refused is asked again")

	asked := l.answer(t, messaging.AskCode, "11111")
	assert.Equal(t, "the Telegram app", asked.Hint, "where the code was sent")
	assert.True(t, l.answer(t, messaging.AskCode, "22222").Again, "a wrong code is asked again")
	l.answer(t, messaging.AskPassword, "hunter2")

	r := l.end(t)
	require.NoError(t, r.err)
	assert.Equal(t, owner, r.authz)
}

// A number with no Telegram account is not signed up: the linking ends and says so.
func TestALinkByPhoneNeverMakesAnAccount(t *testing.T) {
	t.Parallel()

	l := startLinking(t, true, func(m *tgmock.Mock) {
		m.ExpectCall(&tg.AuthSendCodeRequest{PhoneNumber: "+84900000000", APIID: testAppID, APIHash: testAppHash}).
			ThenResult(&tg.AuthSentCode{Type: &tg.AuthSentCodeTypeSMS{Length: 5}, PhoneCodeHash: "hash"})
		m.ExpectCall(&tg.AuthSignInRequest{PhoneNumber: "+84900000000", PhoneCodeHash: "hash", PhoneCode: "12345"}).
			ThenResult(&tg.AuthAuthorizationSignUpRequired{})
	})

	l.answer(t, messaging.AskPhone, "+84900000000")
	assert.Equal(t, "an SMS", l.answer(t, messaging.AskCode, "12345").Hint)

	r := l.end(t)
	require.Error(t, r.err)
	assert.Contains(t, r.err.Error(), "has no Telegram account")
}

// A linking that ended keeps the account's session under its user id and opens it: the session the link made is the
// one the account connects with, named by its number.
func TestALinkedAccountKeepsItsSession(t *testing.T) {
	t.Parallel()

	svc := openService(t, "", fakeSecrets{})
	ctx := t.Context()

	acc, err := svc.linked(ctx, owner, []byte(`{"Version":1}`))
	require.NoError(t, err)
	t.Cleanup(acc.Close)

	assert.Equal(t, "42", acc.ID())
	assert.Equal(t, "+84912345678", acc.Name())

	data, name, err := svc.store.session(ctx, "42")
	require.NoError(t, err)
	assert.JSONEq(t, `{"Version":1}`, string(data))
	assert.Equal(t, "+84912345678", name)
}

// Without the owner's api_id in the vault no linking starts, and it says the api_id is missing.
func TestNoLinkStartsWithoutTheAPIID(t *testing.T) {
	t.Parallel()

	svc := openService(t, "", fakeSecrets{})

	_, err := svc.Link(t.Context())
	require.ErrorIs(t, err, ErrNoAPIID)

	_, err = svc.LinkByPhone(t.Context())
	require.ErrorIs(t, err, ErrNoAPIID)
}
