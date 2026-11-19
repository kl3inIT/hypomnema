package serviceproxy

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/kl3inIT/hypomnema/internal/shared/githost"
)

func TestDesktopTrustNamesBelongToServiceCLICatalogs(t *testing.T) {
	t.Parallel()
	catalogs := []Catalog{larkCatalog(), google(), githubCatalog(), awsCatalog, gitlabCatalog(githost.GitLabHost)}
	names := ExecTrustHosts()
	for _, name := range names {
		host := name
		if strings.HasPrefix(host, ".") {
			host = "sts" + host
		}
		var relayed bool
		for _, catalog := range catalogs {
			relayed = relayed || catalog.Has(host)
		}
		assert.True(t, relayed, "%s is not in a service CLI catalog", name)
	}

	assert.Contains(t, names, "modelarmor.europe-west1.rep.googleapis.com")
	assert.Contains(t, names, "productionresultssa009.blob.core.windows.net")
	assert.NotContains(t, names, ".blob.core.windows.net")
	assert.NotContains(t, names, ".rep.googleapis.com")
}
