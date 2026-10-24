// Package deploy は配線の正本（messaging.yaml・schedules.yaml）を Go に埋め込む。Terraform は同じファイルを yamldecode で読む。
package deploy

import "embed"

// FS は messaging.yaml と schedules.yaml。
//
//go:embed messaging.yaml schedules.yaml
var FS embed.FS
