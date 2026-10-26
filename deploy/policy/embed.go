// Package policydata embeds the repository-reviewed web domain policy files
// (docs/tool-platform.md §13). There is no runtime download: these files are
// the only source.
package policydata

import "embed"

// FS holds web-domains.yaml, categories/*.txt (+ malware snapshot signatures)
// and keys/*.pub.
//
//go:embed web-domains.yaml categories keys
var FS embed.FS
