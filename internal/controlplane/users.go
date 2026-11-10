package controlplane

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"
)

// The web's users (issue #114): commands of the owner on the server, as
// tokens are. No user is shipped; the password is read from standard input,
// never from an argument, so it stays out of the shell's history (Coder's
// create-admin-user and reset-password).

const usersUsage = "users add <name> | users passwd <name> | users list; the password is read from standard input"

// maxPasswordInput bounds what is read of standard input for a password.
const maxPasswordInput = 1024

func (o *owner) users(ctx context.Context, args []string) error {
	switch {
	case len(args) == 2 && args[0] == "add":
		password, err := o.readPassword()
		if err != nil {
			return err
		}

		u, err := o.svc.AddUser(ctx, args[1], password)
		if err != nil {
			return err
		}

		o.printf("user %s (%s) can sign in to the web\n", u.Name, u.ID)

		return nil
	case len(args) == 2 && args[0] == "passwd":
		password, err := o.readPassword()
		if err != nil {
			return err
		}

		if err := o.svc.SetUserPassword(ctx, args[1], password); err != nil {
			return err
		}

		o.printf("user %s has a new password; every session of theirs has ended\n", args[1])

		return nil
	case len(args) == 1 && args[0] == "list":
		users, err := o.svc.ListUsers(ctx)
		if err != nil {
			return err
		}

		for _, u := range users {
			o.printf("%s\t%s\tpassword set %s\n", u.ID, u.Name, u.UpdatedAt.Format(time.RFC3339))
		}

		return nil
	default:
		return fmt.Errorf("%w: %s", ErrUsage, usersUsage)
	}
}

// readPassword reads one line of standard input.
func (o *owner) readPassword() (string, error) {
	raw, err := io.ReadAll(io.LimitReader(o.in, maxPasswordInput))
	if err != nil {
		return "", fmt.Errorf("reading the password from standard input: %w", err)
	}

	line, _, _ := strings.Cut(string(raw), "\n")

	return strings.TrimSuffix(line, "\r"), nil
}
