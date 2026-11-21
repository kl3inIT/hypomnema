package webpush_test

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// pushService is a fake push service over TLS, as the owner's Second Bite tests its sender against one
// (server/internal/notification/push_test.go): it records what each endpoint was sent and answers each with the
// statuses it is given in turn, then 201.
type pushService struct {
	t   *testing.T
	srv *httptest.Server

	mu       sync.Mutex
	answers  map[string][]int
	received map[string][]received
}

type received struct {
	header http.Header
	body   []byte
}

func newPushService(t *testing.T) *pushService {
	t.Helper()

	p := &pushService{t: t, answers: map[string][]int{}, received: map[string][]received{}}
	p.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		name := strings.TrimPrefix(r.URL.Path, "/push/")

		p.mu.Lock()
		p.received[name] = append(p.received[name], received{header: r.Header.Clone(), body: body})

		status := http.StatusCreated
		if a := p.answers[name]; len(a) > 0 {
			status, p.answers[name] = a[0], a[1:]
		}
		p.mu.Unlock()

		w.WriteHeader(status)
	}))
	t.Cleanup(p.srv.Close)

	return p
}

func (p *pushService) answer(name string, statuses ...int) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.answers[name] = statuses
}

func (p *pushService) got(name string) []received {
	p.mu.Lock()
	defer p.mu.Unlock()

	return append([]received(nil), p.received[name]...)
}

// browser is a browser's side of a subscription: its key pair and auth secret, with which it decrypts a push
// (RFC 8291).
type browser struct {
	endpoint string
	key      *ecdh.PrivateKey
	auth     []byte
}

func (p *pushService) browser(name string) browser {
	key, err := ecdh.P256().GenerateKey(rand.Reader)
	require.NoError(p.t, err)

	auth := make([]byte, 16)
	_, err = rand.Read(auth)
	require.NoError(p.t, err)

	return browser{endpoint: p.srv.URL + "/push/" + name, key: key, auth: auth}
}

func (b browser) p256dh() string { return base64.RawURLEncoding.EncodeToString(b.key.PublicKey().Bytes()) }
func (b browser) authKey() string { return base64.RawURLEncoding.EncodeToString(b.auth) }

// open decrypts an aes128gcm push body as the browser does (RFC 8291 section 3.4, RFC 8188).
func (b browser) open(t *testing.T, body []byte) []byte {
	t.Helper()

	require.Greater(t, len(body), 21)
	salt, idLen := body[:16], int(body[20])
	serverKey, ciphertext := body[21:21+idLen], body[21+idLen:]
	require.Equal(t, uint32(4096), binary.BigEndian.Uint32(body[16:20]))

	server, err := ecdh.P256().NewPublicKey(serverKey)
	require.NoError(t, err)
	secret, err := b.key.ECDH(server)
	require.NoError(t, err)

	keyInfo := "WebPush: info\x00" + string(b.key.PublicKey().Bytes()) + string(serverKey)
	prkKey, err := hkdf.Extract(sha256.New, secret, b.auth)
	require.NoError(t, err)
	ikm, err := hkdf.Expand(sha256.New, prkKey, keyInfo, 32)
	require.NoError(t, err)
	prk, err := hkdf.Extract(sha256.New, ikm, salt)
	require.NoError(t, err)
	cek, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: aes128gcm\x00", 16)
	require.NoError(t, err)
	nonce, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: nonce\x00", 12)
	require.NoError(t, err)

	block, err := aes.NewCipher(cek)
	require.NoError(t, err)
	gcm, err := cipher.NewGCM(block)
	require.NoError(t, err)
	plain, err := gcm.Open(nil, nonce, ciphertext, nil)
	require.NoError(t, err)

	// The last record ends with the delimiter 2, then its padding of zeros.
	plain = []byte(strings.TrimRight(string(plain), "\x00"))
	require.NotEmpty(t, plain)
	require.Equal(t, byte(2), plain[len(plain)-1])

	return plain[:len(plain)-1]
}

// push is a push's payload, the Declarative Web Push form.
type push struct {
	WebPush      int `json:"web_push"`
	Notification struct {
		Title    string `json:"title"`
		Body     string `json:"body"`
		Navigate string `json:"navigate"`
		Tag      string `json:"tag"`
	} `json:"notification"`
}

func (b browser) payload(t *testing.T, r received) push {
	t.Helper()

	var p push
	require.NoError(t, json.Unmarshal(b.open(t, r.body), &p))

	return p
}

// vapid checks a push's VAPID header (RFC 8292): the JWT is signed by the public key it names, for the push
// service's origin, with the control plane's URL as its subject. It returns the public key.
func vapid(t *testing.T, r received, origin, subject string) string {
	t.Helper()

	header := r.header.Get("Authorization")
	require.True(t, strings.HasPrefix(header, "vapid t="), header)

	token, key, ok := strings.Cut(strings.TrimPrefix(header, "vapid t="), ", k=")
	require.True(t, ok, header)

	parts := strings.Split(token, ".")
	require.Len(t, parts, 3)

	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	require.NoError(t, err)

	var claims struct {
		Aud string `json:"aud"`
		Sub string `json:"sub"`
	}
	require.NoError(t, json.Unmarshal(raw, &claims))
	require.Equal(t, origin, claims.Aud)
	require.Equal(t, subject, claims.Sub)

	public, err := base64.RawURLEncoding.DecodeString(key)
	require.NoError(t, err)
	x, y := elliptic.Unmarshal(elliptic.P256(), public) //nolint:staticcheck // ecdsa.PublicKey still takes X and Y.
	require.NotNil(t, x)

	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	require.NoError(t, err)
	require.Len(t, sig, 64)

	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	require.True(t, ecdsa.Verify(&ecdsa.PublicKey{Curve: elliptic.P256(), X: x, Y: y}, digest[:],
		new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])), "signed by the key it names")

	return key
}
