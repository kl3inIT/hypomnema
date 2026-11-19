package serviceproxy

import (
	"fmt"
	"slices"

	"github.com/kl3inIT/hypomnema/internal/shared/githost"
)

// ExecTrustHosts gives the local issuer the DNS names of the service CLI catalogs.
// The Model Armor regions are its published locations (docs.cloud.google.com/model-armor/locations).
// GitHub log storage names are enumerated so the issuer never trusts other Azure tenants.
func ExecTrustHosts() []string {
	names := []string{
		larkHost, githubAPI, githubUploads, githubResults, githost.GitLabHost,
		"." + awsDNS, "." + awsDNS + ".cn",
	}
	names = append(names, googleTableOf().hosts...)
	for _, region := range []string{
		"asia-northeast1", "asia-northeast3", "asia-south1", "asia-southeast1", "australia-southeast2",
		"northamerica-northeast2", "us-central1", "us-east1", "us-east4", "us-west1", "southamerica-east1",
		"europe-southwest1", "europe-west1", "europe-west2", "europe-west3", "europe-west4", "europe-west9", "eu", "us",
	} {
		names = append(names, googleModelArmorPrefix+region+"."+googleRegionalDNS)
	}

	for width, limit := 1, 10; width <= 3; width, limit = width+1, limit*10 {
		for index := range limit {
			names = append(names, fmt.Sprintf("%s%0*d.%s", githubBlobPrefix, width, index, githubBlobDNS))
		}
	}

	slices.Sort(names)
	return slices.Compact(names)
}
