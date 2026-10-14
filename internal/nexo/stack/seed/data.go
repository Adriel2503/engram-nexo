package seed

import "embed"

//go:embed data/* data/*/*
var seedFS embed.FS
