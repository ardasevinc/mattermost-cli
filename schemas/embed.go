package schemas

import "embed"

// FS contains the checked-in public machine contracts shipped with mm.
//
//go:embed v2/*.schema.json v2/examples/*.json
var FS embed.FS
