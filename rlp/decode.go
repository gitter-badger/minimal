package rlp

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/big"
	"reflect"
)

// TODO, need a way to finish the list, otherwise we dont know if it really finished or not

var (
	ErrSize = errors.New("rlp: non-canonical size information")
)

type RLP struct {
	data []byte
	pos  uint

	kind  Kind
	size  uint
	lists []uint

	uintbuf []byte
	len     uint
}

type Kind byte

const (
	Byte Kind = iota + 1
	Bytes
	List
)

func (k *Kind) String() string {
	switch *k {
	case Byte:
		return "Byte"
	case Bytes:
		return "Bytes"
	case List:
		return "List"
	default:
		panic(fmt.Errorf("kind %d not found", k))
	}
}

// Data returns the rlp object
func (r *RLP) Data() []byte {
	return r.data
}

func (r *RLP) Kind() (Kind, uint, error) {
	if r.kind != 0 {
		return r.kind, r.size, nil
	}

	kind, size, err := r.readKind()
	if err != nil {
		return 0, 0, err
	}

	r.kind, r.size = kind, size
	return kind, size, nil
}

// Kind returns the kind of the next item
func (r *RLP) readKind() (Kind, uint, error) {
	cur, err := r.readByte()
	if err != nil {
		return 0, 0, err
	}

	switch {
	case cur < 0x80:
		// 1. its his own value
		return Byte, uint(cur), nil

	case cur < 0xB8:
		// 2. item 55 bytes long
		return Bytes, uint(cur - 0x80), nil

	case cur < 0xC0:
		// 3. item more than 55 bytes long
		size, err := r.readUint(uint(cur - 0xB7))
		if err == nil && size < 56 {
			err = ErrSize
		}
		return Bytes, uint(size), nil

	case cur < 0xF8:
		// 4. list. Total payload is less than 55 bytes
		return List, uint(cur - 0xC0), nil

	default:
		// 5. list. Total payload is more than 55 bytes
		size, err := r.readUint(uint(cur - 0xf7))
		if err == nil && size < 56 {
			err = ErrSize
		}
		return List, uint(size), err
	}
}

// IsBytes checks that the next element is a string
func (r *RLP) IsBytes() (uint, error) {
	kind, size, err := r.Kind()
	if err != nil {
		return 0, err
	}
	if kind != Bytes {
		return 0, typeErr(Bytes, kind)
	}
	return size, nil
}

// Bytes returns the next item in bytes format and consumes it
func (r *RLP) Bytes() ([]byte, error) {
	kind, size, err := r.Kind()
	if err != nil {
		return nil, err
	}
	if kind == List {
		return nil, fmt.Errorf("list not expected")
	}
	if kind == Byte {
		r.kind = 0
		return []byte{byte(size)}, nil
	}

	// Bytes
	if err := r.checkBounds(r.pos + size); err != nil {
		return nil, err
	}

	b := make([]byte, size)
	copy(b[:], r.data[r.pos:r.pos+size])

	r.pos += size
	r.kind = 0
	return b, nil
}

func (r *RLP) String() (string, error) {
	b, err := r.Bytes()
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func (r *RLP) Uint() (uint64, error) {
	return r.uint(64)
}

func (r *RLP) uint(maxbits int) (uint64, error) {
	b, err := r.Bytes()
	if err != nil {
		return 0, err
	}

	size := uint(len(b))
	if size == 1 {
		return uint64(b[0]), nil
	}

	// string
	if size > uint(maxbits/8) {
		return 0, fmt.Errorf("uint overflow")
	}

	r.pos -= size
	return r.readUint(size) // TODO, there are more checks here
}

func (r *RLP) BigInt() (*big.Int, error) {
	b, err := r.Bytes()
	if err != nil {
		return nil, err
	}
	if len(b) > 0 && b[0] == 0 {
		return nil, fmt.Errorf("leading zero bytes")
	}
	return big.NewInt(1).SetBytes(b), nil
}

// IsBytesOfSize checks that the bytes item has a given length
func (r *RLP) IsBytesOfSize(n uint) (bool, error) {
	s, err := r.IsBytes()
	if err != nil {
		return false, err
	}
	return s == n, nil
}

// IsAddress checks if the next element is an address
func (r *RLP) IsAddress() (bool, error) {
	return r.IsBytesOfSize(40)
}

// List starts a list element
func (r *RLP) List() (uint, error) {
	kind, size, err := r.Kind()
	if err != nil {
		return 0, err
	}
	if kind != List {
		return 0, typeErr(List, kind)
	}

	r.kind = 0
	r.lists = append([]uint{r.pos + size}, r.lists...)
	return size, nil
}

// EndList indicates the end of the list element
func (r *RLP) EndList() error {
	if len(r.lists) == 0 {
		return fmt.Errorf("it was not inside a list")
	}

	v := r.lists[0]
	if v != r.pos {
		return fmt.Errorf("bad ending of the list, expected %d but found %d", v, r.pos)
	}

	r.lists = r.lists[1:]
	return nil
}

func (r *RLP) readUint(size uint) (uint64, error) {
	switch size {
	case 0:
		return 0, nil

	case 1:
		b, err := r.readByte()
		return uint64(b), err

	default:
		start := int(8 - size)
		for i := 0; i < start; i++ {
			r.uintbuf[i] = 0
		}

		if err := r.checkBounds(r.pos + size); err != nil {
			return 0, err
		}
		copy(r.uintbuf[start:], r.data[r.pos:r.pos+size])
		r.pos = r.pos + size

		return binary.BigEndian.Uint64(r.uintbuf), nil
	}
}

func (r *RLP) checkBounds(size uint) error {
	end := r.len
	if len(r.lists) != 0 {
		end = r.lists[0]
	}

	if size > end {
		return io.EOF
	}
	return nil
}

func (r *RLP) readByte() (byte, error) {
	if err := r.checkBounds(r.pos); err != nil {
		return 0, err
	}

	v := r.data[r.pos]
	r.pos++
	return v, nil
}

// NewRLP creates a new rlp reference
func NewRLP(data []byte) *RLP {
	rlp := &RLP{
		data:    data,
		pos:     0,
		kind:    0,
		size:    0,
		uintbuf: make([]byte, 8),
		len:     uint(len(data)),
		lists:   []uint{},
	}
	return rlp
}

func typeErr(expected Kind, kind Kind) error {
	return fmt.Errorf("expected %s but found %s", expected.String(), kind.String())
}

func Decode(data []byte, val interface{}) error {
	v := reflect.ValueOf(val)
	if v.Kind() != reflect.Ptr {
		return fmt.Errorf("expected a pointer")
	}

	rlp := NewRLP(data)
	return rlp.decode(v.Elem())
}

func (r *RLP) decode(v reflect.Value) error {
	switch kind := v.Kind(); kind {

	default:
		return fmt.Errorf("Decode type %s not supported", kind.String())
	}
}
