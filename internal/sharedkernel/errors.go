// Package sharedkernel は全モジュールが共有する値の型（ID・金額・税率・割引・暦日・期間・時刻・イベントの封筒）を持つ。
// 標準ライブラリと github.com/google/uuid だけに依存する（platform.md §3.1）。
package sharedkernel

import "errors"

var (
	ErrInvalidID       = errors.New("sharedkernel: invalid id")
	ErrNegativeMoney   = errors.New("sharedkernel: negative money")
	ErrMoneyOverflow   = errors.New("sharedkernel: money overflow")
	ErrInvalidTaxRate  = errors.New("sharedkernel: invalid tax rate")
	ErrInvalidDate     = errors.New("sharedkernel: invalid date")
	ErrInvalidPeriod   = errors.New("sharedkernel: invalid billing period")
	ErrInvalidDiscount = errors.New("sharedkernel: invalid discount")
)
