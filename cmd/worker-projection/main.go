// Command worker-projection はダッシュボードの集計（reporting の購読）を動かす（console-dashboard 設計 ADR-R04）。
package main

import (
	"os"

	"subscription-platform/cmd/internal/wiring"
)

func main() { os.Exit(wiring.Main("worker-projection", wiring.RunWorker("worker-projection"))) }
