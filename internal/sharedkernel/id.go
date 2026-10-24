package sharedkernel

import (
	"crypto/rand"
	"database/sql/driver"
	"fmt"
	"math/big"

	"github.com/google/uuid"
)

// ID は型付きの識別子。T は使わない幽霊型で、ID[A] と ID[B] は別の型になる（AC-PLT-001）。
type ID[T any] struct{ u uuid.UUID }

// NewID は UUIDv7 の ID を作る。同じプロセスの中では単調に増える。
func NewID[T any]() ID[T] { return ID[T]{u: uuid.Must(uuid.NewV7())} }

// ParseID は文字列の UUID を ID にする。形式が不正なら ErrInvalidID。
func ParseID[T any](s string) (ID[T], error) {
	u, err := uuid.Parse(s)
	if err != nil || len(s) != 36 {
		return ID[T]{}, fmt.Errorf("%w: %q", ErrInvalidID, s)
	}
	return ID[T]{u: u}, nil
}

// IDFromUUID は adapter で uuid.UUID から ID を作る。
func IDFromUUID[T any](u uuid.UUID) ID[T] { return ID[T]{u: u} }

func (id ID[T]) String() string  { return id.u.String() }
func (id ID[T]) IsZero() bool    { return id.u == uuid.Nil }
func (id ID[T]) UUID() uuid.UUID { return id.u }

func (id ID[T]) MarshalText() ([]byte, error) { return []byte(id.u.String()), nil }

func (id *ID[T]) UnmarshalText(b []byte) error {
	p, err := ParseID[T](string(b))
	if err != nil {
		return err
	}
	*id = p
	return nil
}

// Scan は sql.Scanner を実装する。
func (id *ID[T]) Scan(src any) error {
	switch v := src.(type) {
	case nil:
		*id = ID[T]{}
		return nil
	case string:
		return id.UnmarshalText([]byte(v))
	case []byte:
		if len(v) == 16 {
			copy(id.u[:], v)
			return nil
		}
		return id.UnmarshalText(v)
	case [16]byte:
		id.u = v
		return nil
	default:
		return fmt.Errorf("%w: cannot scan %T", ErrInvalidID, src)
	}
}

// Value は driver.Valuer を実装する。ゼロ値は NULL にする。
func (id ID[T]) Value() (driver.Value, error) {
	if id.IsZero() {
		return nil, nil
	}
	return id.u.String(), nil
}

type TenantTag struct{}
type TenantID = ID[TenantTag]

type CustomerAccountTag struct{}
type CustomerAccountID = ID[CustomerAccountTag]

type EventTag struct{}
type EventID = ID[EventTag]

const base62 = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// NewOpaqueID は prefix + "_" + base62 の n 文字を crypto/rand で作る（cus_・evt_ 用）。
func NewOpaqueID(prefix string, n int) string {
	b := make([]byte, n)
	max := big.NewInt(int64(len(base62)))
	for i := range b {
		r, err := rand.Int(rand.Reader, max)
		if err != nil {
			panic(err)
		}
		b[i] = base62[r.Int64()]
	}
	return prefix + "_" + string(b)
}
