package rlp

import (
	"fmt"
	"math/big"
	"reflect"
)

var (
	BigInt = reflect.TypeOf(new(big.Int)).Kind()
)

func EncodeToRLP(in interface{}) ([]byte, error) {
	return encode(reflect.ValueOf(in))
}

func encode(v reflect.Value) ([]byte, error) {
	switch kind := v.Kind(); kind {
	case reflect.Slice:
		if isByte(v) {
			return encodeItem(v.Bytes()), nil
		}
		return encodeSlice(v)

	case reflect.Struct:
		return encodeStruct(v)

	case reflect.String:
		return encodeString(v.String()), nil

	case BigInt:
		return encodeBigInt(v.Interface().(*big.Int))

	case reflect.Ptr:
		return encode(v.Elem())

	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return encodeUint(v.Uint()), nil

	case reflect.Interface:
		return encode(v.Elem())

	default:
		return nil, fmt.Errorf("Encode type %s not supported", kind.String())
	}
}

func encodeUint(i uint64) []byte {
	if i == 0 {
		return []byte{0x80}
	}
	return encodeItem(putUint32(uint(i)))
}

func isByte(v reflect.Value) bool {
	return v.Type().String() == "[]uint8"
}

func encodeBigInt(i *big.Int) ([]byte, error) {
	if i.Sign() < 0 {
		return nil, fmt.Errorf("Cannot encode negative *big.Int %d", i.Int64())
	}
	return encodeItem(i.Bytes()), nil
}

func encodeString(b string) []byte {
	return encodeItem([]byte(b))
}

func encodeStruct(v reflect.Value) ([]byte, error) {
	vs := make([]reflect.Value, v.NumField())
	for i := range vs {
		vs[i] = v.Field(i)
	}
	return encodeList(vs)
}

func encodeSlice(v reflect.Value) ([]byte, error) {
	vs := make([]reflect.Value, v.Len())
	for i := range vs {
		vs[i] = v.Index(i)
	}
	return encodeList(vs)
}

func encodeList(v []reflect.Value) ([]byte, error) {
	if len(v) == 0 {
		return []byte{0xC0}, nil
	}

	data := []byte{}
	for _, i := range v {
		j, err := encode(i)
		if err != nil {
			panic(err)
		}
		data = append(data, j...)
	}

	size := uint(len(data))
	if size < 56 {
		// short list
		return append([]byte{0xC0 + byte(size)}, data...), nil
	}

	// long list
	sizesize := putUint32(size)
	header := []byte{0xf7 + byte(len(sizesize))}
	header = append(header, sizesize...)

	return append(header, data...), nil
}

func encodeItem(b []byte) []byte {
	// check if its only one byte
	if len(b) == 1 && b[0] <= 0x7f {
		return b
	}

	// encode header (at least one item)
	var header []byte

	size := uint(len(b))
	if size < 56 {
		// short item
		header = []byte{0x80 + byte(size)}
	} else {
		// long item
		sizesize := putUint32(size)
		header = []byte{0xB7 + byte(len(sizesize))}
		header = append(header, sizesize...)
	}

	return append(header, b...)
}

func putUint32(num uint) []byte {
	buf := make([]byte, 8)
	n := putint(buf, uint64(num))
	return buf[0:n]
}

func isUint(k reflect.Kind) bool {
	return k >= reflect.Uint && k <= reflect.Uintptr
}

func putint(b []byte, i uint64) (size int) {
	switch {
	case i < (1 << 8):
		b[0] = byte(i)
		return 1
	case i < (1 << 16):
		b[0] = byte(i >> 8)
		b[1] = byte(i)
		return 2
	case i < (1 << 24):
		b[0] = byte(i >> 16)
		b[1] = byte(i >> 8)
		b[2] = byte(i)
		return 3
	case i < (1 << 32):
		b[0] = byte(i >> 24)
		b[1] = byte(i >> 16)
		b[2] = byte(i >> 8)
		b[3] = byte(i)
		return 4
	case i < (1 << 40):
		b[0] = byte(i >> 32)
		b[1] = byte(i >> 24)
		b[2] = byte(i >> 16)
		b[3] = byte(i >> 8)
		b[4] = byte(i)
		return 5
	case i < (1 << 48):
		b[0] = byte(i >> 40)
		b[1] = byte(i >> 32)
		b[2] = byte(i >> 24)
		b[3] = byte(i >> 16)
		b[4] = byte(i >> 8)
		b[5] = byte(i)
		return 6
	case i < (1 << 56):
		b[0] = byte(i >> 48)
		b[1] = byte(i >> 40)
		b[2] = byte(i >> 32)
		b[3] = byte(i >> 24)
		b[4] = byte(i >> 16)
		b[5] = byte(i >> 8)
		b[6] = byte(i)
		return 7
	default:
		b[0] = byte(i >> 56)
		b[1] = byte(i >> 48)
		b[2] = byte(i >> 40)
		b[3] = byte(i >> 32)
		b[4] = byte(i >> 24)
		b[5] = byte(i >> 16)
		b[6] = byte(i >> 8)
		b[7] = byte(i)
		return 8
	}
}
