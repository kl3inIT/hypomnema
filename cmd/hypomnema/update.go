package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"regexp"

	"github.com/kl3inIT/hypomnema/internal/controlplane/selfupdate"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
)

// The `update` command (issue #279): run as root by hypomnema-update.service, which a timer starts, from the
// root-owned /usr/local/bin/hypomnema. It puts a newer release of the control plane in place
// (internal/controlplane/selfupdate).
//
//	hypomnema update --user U --home DIR [--unit U] [--release URL] [--state DIR] [--ready URL]
//	hypomnema update install --from FILE --user U --home DIR ...   (run by update, from the new release)

var unixUser = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

func update(ctx context.Context, args []string, stdout io.Writer) error {
	install := len(args) > 0 && args[0] == "install"
	if install {
		args = args[1:]
	}

	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	o := selfupdate.Options{Current: platform.Version, Out: stdout}
	fs.StringVar(&o.User, "user", "", "the user the control plane runs as")
	fs.StringVar(&o.Home, "home", "", "the control plane's folder, with bin/hypomnema, env and data/")
	fs.StringVar(&o.Unit, "unit", selfupdate.DefaultUnit, "the control plane's systemd unit")
	fs.StringVar(&o.ReleaseURL, "release", selfupdate.DefaultReleaseURL, "where the release's manifests are")
	fs.StringVar(&o.State, "state", selfupdate.DefaultState, "a root-owned folder for the lock and the downloads")
	fs.StringVar(&o.Ready, "ready", selfupdate.DefaultReady, "the control plane's /readyz")
	from := fs.String("from", "", "update install: the new release's binary")

	if err := fs.Parse(args); err != nil {
		return err
	}

	switch {
	case fs.NArg() != 0, !unixUser.MatchString(o.User), !filepath.IsAbs(o.Home), !filepath.IsAbs(o.State),
		install == (*from == ""):
		return fmt.Errorf("%w: update --user U --home DIR (absolute) [--unit U] [--release URL] [--state DIR] "+
			"[--ready URL]", errUsage)
	}

	if install {
		return selfupdate.Install(ctx, o, *from)
	}

	err := selfupdate.Update(ctx, o)
	if errors.Is(err, selfupdate.ErrBusy) {
		_, err = fmt.Fprintln(stdout, "another update is running; this one does nothing")
	}

	return err
}
