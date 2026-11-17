package serviceproxy

import "encoding/json"

// LarkCatalogOf is the catalog a lark_methods.json makes, for comparing one with another.
func LarkCatalogOf(raw []byte) (Catalog, error) {
	var methods []larkMethod
	if err := json.Unmarshal(raw, &methods); err != nil {
		return Catalog{}, err
	}

	return larkCatalogOf(methods), nil
}
