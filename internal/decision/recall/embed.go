package recall

import "embed"

// fixtureFS holds the labeled scenarios. They are synthetic and contain
// no secrets.
//
//go:embed fixtures/*.json
var fixtureFS embed.FS
