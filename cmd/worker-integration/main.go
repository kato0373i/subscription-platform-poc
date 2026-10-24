// Command worker-integration は事業者 Webhook の配信・メール送信を動かす。
package main

import (
	"os"

	"subscription-platform/cmd/internal/wiring"
)

func main() { os.Exit(wiring.Main("worker-integration", wiring.RunWorker("worker-integration"))) }
