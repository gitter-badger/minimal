package tests

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"math/big"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/umbracle/minimal/rlp"
)

var rlpTests = "RLPTests"

type rlpCase struct {
	In  interface{} `json:"in"`
	Out string      `json:"out"`
}

func TestRLP(t *testing.T) {
	files, err := listFiles(rlpTests)
	if err != nil {
		t.Fatal(err)
	}

	for _, file := range files {
		data, err := ioutil.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}

		var c map[string]rlpCase
		if err := json.Unmarshal(data, &c); err != nil {
			t.Fatal(err)
		}

		for name, cc := range c {
			t.Run(name, func(t *testing.T) {
				if cc.In == "VALID" || cc.In == "INVALID" {
					t.Skip()
					return
				}

				input := generateRLPInput(name, cc.In)

				data := hexutil.MustDecode(cc.Out)
				r := rlp.NewRLP(data)

				// Decoding
				if err := validateDecoding(r, input); err != nil {
					t.Fatal("")
				}

				// Encoding
				res, err := rlp.EncodeToRLP(input)
				if err != nil {
					t.Fatal(err)
				}

				if !bytes.Equal(res, data) {
					t.Fatal("bad encoding")
				}
			})
		}
	}
}

func validateDecoding(r *rlp.RLP, v interface{}) error {
	switch obj := v.(type) {
	case uint64:
		i, err := r.Uint()
		if err != nil {
			return fmt.Errorf("err in uint: %v", err)
		}
		if i != obj {
			return fmt.Errorf("result mismatch, found %d but expected %d", i, obj)
		}

	case *big.Int:
		i, err := r.BigInt()
		if err != nil {
			return fmt.Errorf("err in bigint: %v", err)
		}
		if i.Cmp(obj) != 0 {
			return fmt.Errorf("result mismatch, found %s but expected %s", i.String(), obj.String())
		}

	case []byte:
		b, err := r.Bytes()
		if err != nil {
			return fmt.Errorf("err in bytes: %v", err)
		}
		if !bytes.Equal(b, obj) {
			return fmt.Errorf("result mismatch, found %s but expected %s", hexutil.Encode(b), hexutil.Encode(obj))
		}

	case string:
		s, err := r.String()
		if err != nil {
			return fmt.Errorf("err in bytes: %v", err)
		}
		if s != obj {
			return fmt.Errorf("result mismatch, found %s but expected %s", s, obj)
		}

	case []interface{}:
		if _, err := r.List(); err != nil {
			return fmt.Errorf("err in list: %v", err)
		}
		for i, v := range obj {
			if err := validateDecoding(r, v); err != nil {
				return fmt.Errorf("result mismatch, array at index %d: %v", i, err)
			}
		}
		if err := r.EndList(); err != nil {
			return fmt.Errorf("err in end list: %v", err)
		}

	default:
		panic(fmt.Errorf("unhandled type: %T", v))
	}
	return nil
}

func generateRLPInput(name string, v interface{}) interface{} {
	switch v := v.(type) {
	case float64:
		return uint64(v)

	case string:
		if strings.HasPrefix(name, "bytes") {
			// one of the bytes test, return byte object
			return []byte(v)
		}
		if len(v) > 0 && v[0] == '#' { // # big int starts with a faux
			big, ok := new(big.Int).SetString(v[1:], 10)
			if !ok {
				panic(fmt.Errorf("bad test: bad big int: %q", v))
			}
			return big
		}
		return v

	case []interface{}:
		new := make([]interface{}, len(v))
		for i := range v {
			new[i] = generateRLPInput(name, v[i])
		}
		return new

	default:
		panic(fmt.Errorf("can't handle %T", v))
	}
}
