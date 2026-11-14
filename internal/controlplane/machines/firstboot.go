package machines

import (
	_ "embed"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"text/template"
)

//go:embed firstboot.sh.tmpl
var firstBootSource string

var firstBootTemplate = template.Must(template.New("firstboot").Funcs(template.FuncMap{
	"shellQuote": shellQuote,
}).Parse(firstBootSource))

// installPath is where the control plane serves the node's install script
// (nodeapi.PathInstallScript).
const installPath = "/install/node.sh"

// maxUserDataBytes is the size EC2 takes as user data before base64.
const maxUserDataBytes = 16 * 1024

// ErrNoPublicURL says the control plane does not know the URL machines reach it at.
var ErrNoPublicURL = errors.New("the control plane's public URL is not set or is not an https URL")

// FirstBoot returns the script a new machine runs once: it installs the node
// from the control plane at publicURL and gives it token. The script holds
// the token, so it is passed to the provider and kept nowhere else. For a
// device (DeviceAndroid) it also installs what the emulator needs and tells
// the node it holds one (issue #417); empty for a plain machine.
func FirstBoot(publicURL, token, device string) (string, error) {
	u, err := url.Parse(strings.TrimRight(strings.TrimSpace(publicURL), "/"))
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", ErrNoPublicURL
	}

	if token == "" {
		return "", errors.New("a first-boot script needs the node's token")
	}

	var b strings.Builder

	err = firstBootTemplate.Execute(&b, struct{ Token, InstallURL, Device string }{
		Token: token, InstallURL: u.String() + installPath, Device: device,
	})
	if err != nil {
		return "", fmt.Errorf("rendering the first-boot script: %w", err)
	}

	if b.Len() > maxUserDataBytes {
		return "", fmt.Errorf("the first-boot script is %d bytes, more than %d", b.Len(), maxUserDataBytes)
	}

	return b.String(), nil
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
