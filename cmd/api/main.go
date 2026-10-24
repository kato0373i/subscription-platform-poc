// Command api は HTTP の入口（/v1/me・/v1/console・/v1/tenant・/v1/ops・/v1/checkout・/webhooks、devtools では /v1/dev）。
package main

import (
	"os"

	"subscription-platform/cmd/internal/wiring"
)

func main() { os.Exit(wiring.Main("api", wiring.RunAPI)) }
