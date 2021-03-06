package tests

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"math/big"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
	"github.com/umbracle/minimal/evm"
)

var vmTests = "VMTests"

type VMCase struct {
	Info *info `json:"_info"`
	Env  *env  `json:"env"`
	Exec *exec `json:"exec"`

	Gas  string `json:"gas"`
	Logs string `json:"logs"`
	Out  string `json:"out"`

	Post stateSnapshop `json:"post"`
	Pre  stateSnapshop `json:"pre"`
}

func testVMCase(t *testing.T, name string, c *VMCase) {
	env := c.Env.ToEnv(t)
	env.GasPrice = c.Exec.GasPrice

	fmt.Println("-------------------------------------")
	fmt.Println(name)

	initialCall := true
	canTransfer := func(state *state.StateDB, address common.Address, amount *big.Int) bool {
		if initialCall {
			initialCall = false
			return true
		}
		return evm.CanTransfer(state, address, amount)
	}

	transfer := func(state *state.StateDB, from, to common.Address, amount *big.Int) error {
		return nil
	}

	state := buildState(t, c.Pre)

	e := evm.NewEVM(state, env, params.MainnetChainConfig, params.GasTableHomestead, vmTestBlockHash)
	e.CanTransfer = canTransfer
	e.Transfer = transfer

	fmt.Printf("BlockNumber: %s\n", c.Env.Number)
	fmt.Println(c.Exec.Code)

	ret, gas, err := e.Call(c.Exec.Caller, c.Exec.Address, c.Exec.Data, c.Exec.Value, c.Exec.GasLimit)

	fmt.Println(name)
	if c.Gas == "" {
		if err == nil {
			t.Fatalf("gas unspecified (indicating an error), but VM returned no error")
		}
		if gas > 0 {
			t.Fatalf("gas unspecified (indicating an error), but VM returned gas remaining > 0")
		}
		return
	}

	// check return
	if c.Out == "" {
		c.Out = "0x"
	}
	if ret := hexutil.Encode(ret); ret != c.Out {
		t.Fatalf("return mismatch: got %s, want %s", ret, c.Out)
	}

	// check logs
	if logs := rlpHash(state.Logs()); logs != common.HexToHash(c.Logs) {
		t.Fatalf("logs hash mismatch: got %x, want %x", logs, c.Logs)
	}

	// check state
	for i, account := range c.Post {
		addr := stringToAddressT(t, i)

		for k, v := range account.Storage {
			key := common.HexToHash(k)
			val := common.HexToHash(v)

			if have := state.GetState(addr, key); have != val {
				t.Fatalf("wrong storage value at %x:\n  got  %x\n  want %x", k, have, val)
			}
		}
	}

	// check remaining gas
	if expected := stringToUint64T(t, c.Gas); gas != expected {
		t.Fatalf("gas remaining mismatch: got %d want %d", gas, expected)
	}
}

func TestEVM(t *testing.T) {
	folders, err := listFolders(vmTests)
	if err != nil {
		t.Fatal(err)
	}

	long := []string{
		"loop-",
		"vmPerformance",
	}

	for _, folder := range folders {
		files, err := listFiles(folder)
		if err != nil {
			t.Fatal(err)
		}

		for _, file := range files {
			t.Run(folder, func(t *testing.T) {
				if !strings.HasSuffix(file, ".json") {
					return
				}

				data, err := ioutil.ReadFile(file)
				if err != nil {
					t.Fatal(err)
				}

				var vmcases map[string]*VMCase
				if err := json.Unmarshal(data, &vmcases); err != nil {
					t.Fatal(err)
				}

				for name, cc := range vmcases {
					if contains(long, name) && testing.Short() {
						t.Skip()
						continue
					}

					testVMCase(t, name, cc)
				}
			})
		}
	}
}

func vmTestBlockHash(n uint64) common.Hash {
	return common.BytesToHash(crypto.Keccak256([]byte(big.NewInt(int64(n)).String())))
}
