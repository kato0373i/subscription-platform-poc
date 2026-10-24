// Command worker-billing は課金系の購読者・回収・Outbox の中継・課金系ジョブを動かす。
package main

import (
	"os"

	"subscription-platform/cmd/internal/wiring"
)

func main() { os.Exit(wiring.Main("worker-billing", wiring.RunWorker("worker-billing"))) }
