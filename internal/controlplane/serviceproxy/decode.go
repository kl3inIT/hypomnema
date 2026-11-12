package serviceproxy

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/mail"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
)

// A held request is shown to the owner decoded, not as bytes: a Gmail send
// as To, Subject, Body and attachment names (Onyx's GmailRawMimeDecoder and
// PayloadView). The same decoded form is what a repeat of the request is
// recognized by. Two runs of one command differ in their bytes, in the
// message's Date and Message-Id and in the boundary between parts, and are
// the same in every field shown here (tried on 2026-11-11, issue #154).

const (
	// maxShown bounds one value shown to the owner; what is past it is
	// still part of the digest.
	maxShown = 4000
	// maxFields bounds the lines of one decoded request.
	maxFields = 24
)

// decoded is a request as the owner reads it.
type decoded struct {
	// Subject is what the request is about, for the title: who a mail goes
	// to, a file's name.
	Subject string
	Fields  []orchestration.Field
	// Hash is the digest of everything decoded, shown or not.
	Hash string
}

// decode reads a request. It never fails: a body it cannot read is shown by
// its size and matched by its bytes.
func decode(method, host, path, query, contentType string, body []byte) decoded {
	d := &decoder{digest: sha256.New()}
	d.mix("request", method+" "+host+path+"?"+sortedQuery(query))

	mediaType, params, _ := mime.ParseMediaType(contentType)

	switch {
	case len(body) == 0:
	case strings.HasPrefix(mediaType, "multipart/") && params["boundary"] != "":
		d.multipart(body, params["boundary"])
	case mediaType == "message/rfc822":
		d.message(body)
	case mediaType == "application/json" || json.Valid(body):
		d.json(body)
	default:
		d.opaque("Content", mediaType, body)
	}

	if len(d.fields) > maxFields {
		d.fields = append(d.fields[:maxFields], orchestration.Field{Label: "…", Value: "more than is shown here"})
	}

	return decoded{Subject: d.subject, Fields: d.fields, Hash: hex.EncodeToString(d.digest.Sum(nil))}
}

type decoder struct {
	subject string
	fields  []orchestration.Field
	digest  interface {
		io.Writer
		Sum(b []byte) []byte
	}
}

// mix adds a named value to the digest, with its length so that two values
// cannot run together.
func (d *decoder) mix(name, value string) {
	// A hash's Write never fails.
	_, _ = fmt.Fprintf(d.digest, "%s\x00%d\x00%s\x00", name, len(value), value)
}

// show adds a line the owner reads; the whole value is in the digest.
func (d *decoder) show(label, value string) {
	if value == "" {
		return
	}

	d.mix(label, value)
	d.fields = append(d.fields, orchestration.Field{Label: label, Value: clip(value)})
}

func clip(value string) string {
	if len(value) <= maxShown {
		return value
	}

	cut := maxShown
	for cut > 0 && !utf8.RuneStart(value[cut]) {
		cut--
	}

	return value[:cut] + "…"
}

// opaque shows a body that has no fields by its kind and size, and matches
// it by its bytes.
func (d *decoder) opaque(label, mediaType string, body []byte) {
	sum := sha256.Sum256(body)
	d.mix(label+" bytes", hex.EncodeToString(sum[:]))

	if mediaType == "" {
		mediaType = "data"
	}

	d.fields = append(d.fields, orchestration.Field{Label: label, Value: mediaType + ", " + size(len(body))})
}

func size(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d bytes", n)
	}
}

// multipart reads an upload: the metadata as JSON, then a message or a file.
func (d *decoder) multipart(body []byte, boundary string) {
	reader := multipart.NewReader(bytes.NewReader(body), boundary)

	for {
		part, err := reader.NextPart()
		if err != nil {
			return
		}

		data, err := io.ReadAll(part)
		if err != nil {
			return
		}

		mediaType, _, _ := mime.ParseMediaType(part.Header.Get("Content-Type"))

		switch {
		case mediaType == "message/rfc822":
			d.message(data)
		case mediaType == "application/json":
			d.json(data)
		case len(data) > 0:
			d.file("File", part.FileName(), mediaType, data)
		}
	}
}

// file shows a file by its name, kind and size, and matches its content.
func (d *decoder) file(label, name, mediaType string, data []byte) {
	sum := sha256.Sum256(data)
	d.mix(label+" content", hex.EncodeToString(sum[:]))

	said := mediaType + ", " + size(len(data))
	if name != "" {
		said = name + " (" + said + ")"
	}

	d.fields = append(d.fields, orchestration.Field{Label: label, Value: said})
}

// The headers of a message that say where it goes and what it is, in the
// order the owner reads them. Every other header is shown after them, under
// its own name: nothing a message carries is left out of what is approved,
// except what is made anew for every send or only describes the encoding.
var mailHeaders = []string{"From", "To", "Cc", "Bcc", "Reply-To", "Subject", "In-Reply-To"}

// addressHeaders are the headers that hold addresses, which are shown as a
// person writes them.
var addressHeaders = []string{"From", "To", "Cc", "Bcc", "Reply-To"}

// addresses writes a list of addresses so that where a mail goes cannot be
// misread: the address first, as the service will use it, and then the name
// it was given, quoted, so that a name written to look like an address stays
// a name. A tool writes "<owner@example.com>" for an address without a name;
// that is shown as the address alone. ok is false for a value that is not a
// list of addresses, which is then shown as it was sent.
func addresses(value string) (shown string, ok bool) {
	list, err := mail.ParseAddressList(value)
	if err != nil || len(list) == 0 {
		return "", false
	}

	out := make([]string, 0, len(list))

	for _, a := range list {
		// An address made of ordinary characters is shown bare. Any other, such as one whose mailbox name is
		// quoted and holds a comma or a bracket, is quoted whole, so that it cannot read as two addresses.
		address := a.Address
		if !plainAddress.MatchString(address) {
			address = strconv.Quote(address)
		}

		if a.Name == "" {
			out = append(out, address)

			continue
		}

		out = append(out, fmt.Sprintf("%s (%q)", address, a.Name))
	}

	return strings.Join(out, ", "), true
}

// plainAddress matches an address whose mailbox name and domain hold only
// characters that mean nothing in a list of addresses.
var plainAddress = regexp.MustCompile("^[A-Za-z0-9.!#$%&'*+/=?^_`{|}~-]+@[A-Za-z0-9.-]+$")

var unshownHeaders = map[string]bool{
	"Date": true, "Message-Id": true, "Mime-Version": true, "Content-Type": true,
	"Content-Transfer-Encoding": true, "User-Agent": true,
}

// message reads a mail as it will be sent.
func (d *decoder) message(raw []byte) {
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		d.opaque("Message", "message/rfc822", raw)

		return
	}

	words := new(mime.WordDecoder)

	// A header written twice is shown whole: a service may send to every line of it, and the first alone
	// would hide the rest.
	header := func(name string) string {
		values := make([]string, 0, len(msg.Header[name]))

		for _, value := range msg.Header[name] {
			// Addresses are read from the header as it was sent: decoding its words first would let a name
			// that was written encoded turn into what looks like another address. One that cannot be read
			// as addresses is shown as sent, undecoded, and said to be unreadable: the owner is not shown a
			// guess at where the mail goes.
			if slices.Contains(addressHeaders, name) {
				shown, ok := addresses(value)
				if !ok {
					shown = "not readable as addresses, shown as sent: " + strconv.Quote(value)
				}

				values = append(values, shown)

				continue
			}

			if plain, err := words.DecodeHeader(value); err == nil {
				value = plain
			}

			values = append(values, value)
		}

		return strings.Join(values, ", ")
	}

	for _, name := range mailHeaders {
		d.show(name, header(name))

		if name == "To" && d.subject == "" {
			d.subject = header(name)
		}
	}

	others := make([]string, 0, len(msg.Header))

	for name := range msg.Header {
		if !unshownHeaders[name] && !slices.Contains(mailHeaders, name) {
			others = append(others, name)
		}
	}

	sort.Strings(others)

	for _, name := range others {
		d.show(name, header(name))
	}

	body, err := io.ReadAll(msg.Body)
	if err != nil {
		return
	}

	d.part(msg.Header.Get("Content-Type"), msg.Header.Get("Content-Transfer-Encoding"), "", body)
}

// part reads one part of a mail: its text is the body, anything else an
// attachment.
func (d *decoder) part(contentType, encoding, filename string, body []byte) {
	mediaType, params, _ := mime.ParseMediaType(contentType)
	if mediaType == "" {
		mediaType = "text/plain"
	}

	if strings.HasPrefix(mediaType, "multipart/") && params["boundary"] != "" {
		reader := multipart.NewReader(bytes.NewReader(body), params["boundary"])

		for {
			p, err := reader.NextPart()
			if err != nil {
				return
			}

			// multipart.Reader decodes quoted-printable itself and removes the header; base64 is left to be
			// decoded here.
			data, err := io.ReadAll(p)
			if err != nil {
				return
			}

			d.part(p.Header.Get("Content-Type"), p.Header.Get("Content-Transfer-Encoding"), p.FileName(), data)
		}
	}

	data := transferDecoded(encoding, body)

	if filename == "" && strings.HasPrefix(mediaType, "text/") {
		label := "Body"
		if mediaType != "text/plain" {
			label = "Body (" + strings.TrimPrefix(mediaType, "text/") + ")"
		}

		d.show(label, strings.TrimSpace(string(data)))

		return
	}

	d.file("Attachment", filename, mediaType, data)
}

// json shows a JSON body's top-level values, and matches it whatever the
// order of its keys.
func (d *decoder) json(body []byte) {
	var value any
	if err := json.Unmarshal(body, &value); err != nil {
		d.opaque("Content", "application/json", body)

		return
	}

	object, ok := value.(map[string]any)
	if !ok {
		d.show("Content", canonical(value))

		return
	}

	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}

	sort.Strings(keys)

	for _, key := range keys {
		text, isText := object[key].(string)
		if !isText {
			text = canonical(object[key])
		}

		if key == "raw" {
			// Gmail's JSON form of a send: the whole message, base64url.
			if message, ok := base64URL(text); ok {
				d.message(message)

				continue
			}
		}

		if (key == "name" || key == "summary" || key == "title") && d.subject == "" {
			d.subject = text
		}

		d.show(key, text)
	}
}

// canonical is a value that was read from JSON, written back with its keys
// in order. Such a value always encodes.
func canonical(value any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		return ""
	}

	return string(raw)
}

// sortedQuery is a query string with its parameters in order, so that the
// same request written another way has the same digest.
func sortedQuery(raw string) string {
	values, err := url.ParseQuery(raw)
	if err != nil {
		return raw
	}

	return values.Encode()
}
