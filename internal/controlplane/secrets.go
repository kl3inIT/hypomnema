package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/vault"
)

// The vault's commands (issue #66). A value comes from a file or standard
// input, never from an argument, which other users of the host can read in
// /proc; nothing here prints one.

// maxSecretFileBytes bounds what `--from-file` reads.
const maxSecretFileBytes = 1 << 20

const secretsUsage = "secrets keygen [id] | secrets add <name> --from-file FILE|- [--kind generic|oauth_token_set] " +
	"[--meta key=value]… | secrets list | secrets rotate <name> --from-file FILE|- | secrets rm <name> | " +
	"secrets rewrap"

// secretsKeygen prints a new root key as an entry for HYPOMNEMA_VAULT_KEYS.
func secretsKeygen(args []string, stdout io.Writer) error {
	id := "k" + time.Now().UTC().Format("20060102")

	switch len(args) {
	case 0:
	case 1:
		id = args[0]
	default:
		return fmt.Errorf("%w: %s", ErrUsage, secretsUsage)
	}

	entry, err := vault.GenerateKey(id)
	if err != nil {
		return err
	}

	_, err = fmt.Fprintf(stdout, "%s\n\n"+
		"Add this entry to HYPOMNEMA_VAULT_KEYS (entries are separated by commas) and set\n"+
		"HYPOMNEMA_VAULT_ACTIVE_KEY_ID=%s in the control plane's environment. Keep a copy offline, apart from\n"+
		"the database: without the key, every secret sealed under it is lost.\n", entry, id)

	return err
}

func (o *owner) secrets(ctx context.Context, args []string) error {
	usage := fmt.Errorf("%w: %s", ErrUsage, secretsUsage)

	switch {
	case len(args) >= 2 && args[0] == "add":
		return o.secretsAdd(ctx, args[1], args[2:])
	case len(args) >= 2 && args[0] == "rotate":
		return o.secretsRotate(ctx, args[1], args[2:])
	case len(args) == 1 && args[0] == "list":
		return o.secretsList(ctx)
	case len(args) == 2 && args[0] == "rm":
		if err := o.vault.Delete(ctx, args[1]); err != nil {
			return err
		}

		o.printf("secret %s deleted\n", args[1])

		return nil
	case len(args) == 1 && args[0] == "rewrap":
		n, err := o.vault.Rewrap(ctx)
		if err != nil {
			return err
		}

		o.printf("%d versions rewrapped under the active key\n", n)

		return nil
	default:
		return usage
	}
}

// metaFlag collects repeated --meta key=value.
type metaFlag map[string]string

func (m metaFlag) String() string { return "" }

func (m metaFlag) Set(v string) error {
	key, value, ok := strings.Cut(v, "=")
	if !ok || key == "" {
		return errors.New("metadata is key=value")
	}

	m[key] = value

	return nil
}

func (o *owner) secretsAdd(ctx context.Context, name string, args []string) error {
	fs := flag.NewFlagSet("secrets add", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	file := fs.String("from-file", "", "the file holding the value, or - for standard input")
	kind := fs.String("kind", string(vault.KindGeneric), "generic (one value) or oauth_token_set (a JSON object)")
	meta := metaFlag{}
	fs.Var(meta, "meta", "what the owner needs to recognize the secret, as key=value; not secret")

	if err := fs.Parse(args); err != nil || fs.NArg() != 0 || *file == "" {
		return fmt.Errorf("%w: %s", ErrUsage, secretsUsage)
	}

	payload, err := o.readPayload(vault.Kind(*kind), *file)
	if err != nil {
		return err
	}

	s, err := o.vault.Create(ctx, vault.NewSecret{
		Name: name, Kind: vault.Kind(*kind), Payload: payload, Metadata: meta,
	})
	if err != nil {
		return err
	}

	o.printf("secret %s (%s, %s), version %d; refer to it as %s\n", s.Name, s.ID, s.Kind, s.Version, vault.Ref(s.Name))

	return nil
}

func (o *owner) secretsRotate(ctx context.Context, name string, args []string) error {
	fs := flag.NewFlagSet("secrets rotate", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	file := fs.String("from-file", "", "the file holding the new value, or - for standard input")

	if err := fs.Parse(args); err != nil || fs.NArg() != 0 || *file == "" {
		return fmt.Errorf("%w: %s", ErrUsage, secretsUsage)
	}

	current, err := o.vault.Get(ctx, name)
	if err != nil {
		return err
	}

	payload, err := o.readPayload(current.Kind, *file)
	if err != nil {
		return err
	}

	s, err := o.vault.Rotate(ctx, name, payload)
	if err != nil {
		return err
	}

	o.printf("secret %s is now at version %d\n", s.Name, s.Version)

	return nil
}

func (o *owner) secretsList(ctx context.Context) error {
	secrets, err := o.vault.List(ctx)
	if err != nil {
		return err
	}

	for _, s := range secrets {
		key := "key " + s.KeyID
		if s.KeyMissing {
			key += " (not in the keyring: cannot be opened)"
		}

		state := ""

		switch {
		case s.LoginRequired:
			state = "login required"
		case s.AccessTokenExpiresAt != nil:
			state = "access token until " + s.AccessTokenExpiresAt.Format(time.RFC3339)
		}

		meta := make([]string, 0, len(s.Metadata))
		for k, v := range s.Metadata {
			meta = append(meta, k+"="+v)
		}

		slices.Sort(meta)

		o.printf("%s\t%s\tv%d\t%s\t%s\t%s\t%s\n", s.Name, s.Kind, s.Version, key,
			s.UpdatedAt.Format(time.RFC3339), strings.Join(meta, " "), state)
	}

	return nil
}

// readPayload reads a secret's value: for a generic secret the file's content
// without its trailing line break, for a token set a JSON object of strings.
func (o *owner) readPayload(kind vault.Kind, file string) (vault.Payload, error) {
	var (
		raw []byte
		err error
	)

	if file == "-" {
		raw, err = io.ReadAll(io.LimitReader(o.in, maxSecretFileBytes+1))
	} else {
		raw, err = readLimited(file)
	}

	if err != nil {
		return nil, fmt.Errorf("reading the value: %w", err)
	}

	if len(raw) > maxSecretFileBytes {
		return nil, fmt.Errorf("%w: the value is over %d bytes", vault.ErrInvalid, maxSecretFileBytes)
	}

	if kind != vault.KindOAuthTokenSet {
		return vault.Payload{vault.KeyValue: strings.TrimRight(string(raw), "\r\n")}, nil
	}

	payload := vault.Payload{}
	if err := json.Unmarshal(raw, &payload); err != nil {
		// The decoder's message can quote the input, which is the secret.
		return nil, fmt.Errorf("%w: a token set is a JSON object of strings", vault.ErrInvalid)
	}

	return payload, nil
}

func readLimited(path string) ([]byte, error) {
	f, err := os.Open(path) //nolint:gosec // the owner names the file holding the value
	if err != nil {
		return nil, err
	}
	defer f.Close() //nolint:errcheck // Read-only; nothing to flush.

	return io.ReadAll(io.LimitReader(f, maxSecretFileBytes+1))
}
