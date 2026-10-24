// このパッケージはわざとコンパイルに失敗する（AC-PLT-001）。ID[SubTag] を受ける関数に ID[InvTag] を渡している。
package main

import "subscription-platform/internal/sharedkernel"

type SubTag struct{}
type InvTag struct{}

func takeSubscription(id sharedkernel.ID[SubTag]) string { return id.String() }

func main() {
	inv := sharedkernel.NewID[InvTag]()
	_ = takeSubscription(inv)
}
