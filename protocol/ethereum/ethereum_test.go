package ethereum

import (
	"fmt"
	"io"
	"io/ioutil"
	"math/big"
	"reflect"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/umbracle/minimal/blockchain"
	"github.com/umbracle/minimal/network"
	"github.com/umbracle/minimal/protocol"
)

func testEthHandshake(t *testing.T, s0 *network.Server, ss0 *Status, b0 *blockchain.Blockchain, s1 *network.Server, ss1 *Status, b1 *blockchain.Blockchain) (*Ethereum, *Ethereum) {
	st0 := func() (*Status, error) {
		return ss0, nil
	}

	st1 := func() (*Status, error) {
		return ss1, nil
	}

	var eth0 *Ethereum
	c0 := func(s network.Conn, p *network.Peer) protocol.Handler {
		eth0 = NewEthereumProtocol(s, p, st0, b0)
		return eth0
	}

	var eth1 *Ethereum
	c1 := func(s network.Conn, p *network.Peer) protocol.Handler {
		eth1 = NewEthereumProtocol(s, p, st1, b1)
		return eth1
	}

	s0.RegisterProtocol(protocol.ETH63, c0)
	s1.RegisterProtocol(protocol.ETH63, c1)

	s0.Dial(s1.Enode)

	time.Sleep(500 * time.Millisecond)
	return eth0, eth1
}

var status = Status{
	ProtocolVersion: 63,
	NetworkID:       1,
	TD:              big.NewInt(1),
	CurrentBlock:    common.HexToHash("1"),
	GenesisBlock:    common.HexToHash("1"),
}

func TestHandshake(t *testing.T) {

	// Networkid is different
	status1 := status
	status1.NetworkID = 2

	// Current block is different
	status2 := status
	status2.CurrentBlock = common.HexToHash("2")

	// Genesis block is different
	status3 := status
	status3.GenesisBlock = common.HexToHash("2")

	cases := []struct {
		Status0  *Status
		Status1  *Status
		Expected bool
	}{
		{
			&status,
			&status,
			true,
		},
		{
			&status,
			&status1,
			false,
		},
		{
			&status,
			&status2,
			true,
		},
		{
			&status,
			&status3,
			false,
		},
	}

	for _, cc := range cases {
		s0, s1 := network.TestServers()
		eth0, eth1 := testEthHandshake(t, s0, cc.Status0, nil, s1, cc.Status1, nil)

		// Both handshake fail
		evnt0, evnt1 := <-s0.EventCh, <-s1.EventCh
		if cc.Expected && evnt0.Type != network.NodeJoin {
			t.Fatal("expected to work but not")
		}
		if cc.Expected && evnt1.Type != network.NodeJoin {
			t.Fatal("expected to work but not")
		}

		// If it worked, check if the status message we get is the good one
		if cc.Expected {
			if !reflect.DeepEqual(eth0.status, cc.Status1) {
				t.Fatal("bad")
			}
			if !reflect.DeepEqual(eth1.status, cc.Status0) {
				t.Fatal("bad")
			}
		}
	}
}

func TestHandshakeMsgPostHandshake(t *testing.T) {
	// After the handshake we dont accept more handshake messages
	s0, s1 := network.TestServers()
	eth0, _ := testEthHandshake(t, s0, &status, nil, s1, &status, nil)

	if err := eth0.conn.WriteMsg(StatusMsg); err != nil {
		t.Fatal(err)
	}

	time.Sleep(500 * time.Millisecond)

	// Check if they are still connected
	p0 := s0.GetPeer(s1.ID().String())
	if p0.Connected == true {
		t.Fatal("should be disconnected")
	}

	p1 := s1.GetPeer(s0.ID().String())
	if p1.Connected == true {
		t.Fatal("should be disconnected")
	}
}

func headersToNumbers(headers []*types.Header) []int {
	n := []int{}
	for _, h := range headers {
		n = append(n, int(h.Number.Int64()))
	}
	return n
}

func TestEthereumBlockHeadersMsg(t *testing.T) {
	headers := blockchain.NewTestHeaderChain(100)

	b0 := blockchain.NewTestBlockchain(t, headers)
	b1 := blockchain.NewTestBlockchain(t, headers)

	s0, s1 := network.TestServers()
	eth0, _ := testEthHandshake(t, s0, &status, b0, s1, &status, b1)

	var cases = []struct {
		origin   interface{}
		amount   uint64
		skip     uint64
		reverse  bool
		expected []int
	}{
		{
			headers[1].Hash(),
			10,
			4,
			false,
			[]int{1, 6, 11, 16, 21, 26, 31, 36, 41, 46},
		},
		{
			1,
			10,
			4,
			false,
			[]int{1, 6, 11, 16, 21, 26, 31, 36, 41, 46},
		},
		{
			1,
			10,
			0,
			false,
			[]int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10},
		},
	}

	for _, cc := range cases {
		t.Run("", func(tt *testing.T) {
			ack := make(chan network.AckMessage, 1)
			eth0.Conn().SetHandler(BlockHeadersMsg, ack, 5*time.Second)

			var err error
			if reflect.TypeOf(cc.origin).Name() == "Hash" {
				err = eth0.RequestHeadersByHash(cc.origin.(common.Hash), cc.amount, cc.skip, cc.reverse)
			} else {
				err = eth0.RequestHeadersByNumber(uint64(cc.origin.(int)), cc.amount, cc.skip, cc.reverse)
			}

			if err != nil {
				tt.Fatal(err)
			}

			resp := <-ack
			if resp.Complete {
				var result []*types.Header
				if err := rlp.Decode(resp.Payload, &result); err != nil {
					tt.Fatal(err)
				}

				if !reflect.DeepEqual(headersToNumbers(result), cc.expected) {
					tt.Fatal("expected numbers dont match")
				}
			} else {
				tt.Fatal("failed to receive the headers")
			}
		})
	}
}

func TestEthereumEmptyResponseBodyAndReceipts(t *testing.T) {
	// There are no body and no receipts, the answer is empty
	headers := blockchain.NewTestHeaderChain(100)

	read := func(r io.Reader) []byte {
		data, err := ioutil.ReadAll(r)
		if err != nil {
			t.Fatalf("failed to consume io.Reader: %v", err)
		}
		return data
	}

	b0 := blockchain.NewTestBlockchain(t, headers)
	b1 := blockchain.NewTestBlockchain(t, headers)

	s0, s1 := network.TestServers()
	eth0, _ := testEthHandshake(t, s0, &status, b0, s1, &status, b1)

	batch := []common.Hash{
		headers[0].Hash(),
		headers[5].Hash(),
		headers[10].Hash(),
	}

	// bodies

	ack := make(chan network.AckMessage, 1)
	eth0.Conn().SetHandler(BlockBodiesMsg, ack, 5*time.Second)

	if err := eth0.RequestBodies(batch); err != nil {
		t.Fatal(err)
	}
	if resp := <-ack; resp.Complete {
		if len(read(resp.Payload)) != 1 {
			t.Fatal("there is some content in bodies")
		}
	} else {
		t.Fatal("body request failed")
	}

	// receipts

	ack = make(chan network.AckMessage, 1)
	eth0.Conn().SetHandler(ReceiptsMsg, ack, 5*time.Second)

	if err := eth0.RequestReceipts(batch); err != nil {
		t.Fatal(err)
	}
	if resp := <-ack; resp.Complete {
		if len(read(resp.Payload)) != len(batch)+1 { // when the response is one byte + one byte per empty element
			t.Fatal("there is some content in receipts")
		}
	} else {
		t.Fatal("body request failed")
	}
}

func TestEthereumBody(t *testing.T) {
	b0 := blockchain.NewTestBlockchain(t, blockchain.NewTestHeaderChain(100))

	headers, blocks, receipts := blockchain.NewTestBodyChain(3) // only s1 needs to have bodies and receipts
	b1 := blockchain.NewTestBlockchainWithBlocks(t, blocks, receipts)

	s0, s1 := network.TestServers()
	eth0, _ := testEthHandshake(t, s0, &status, b0, s1, &status, b1)

	// NOTE, we use tx to check if the response is correct, genesis does not
	// have any tx so if we use that one it will fail
	batch := []uint64{2}

	msg := []common.Hash{}
	for _, i := range batch {
		msg = append(msg, headers[i].Hash())
	}

	// -- bodies --

	ack := make(chan network.AckMessage, 1)
	eth0.Conn().SetHandler(BlockBodiesMsg, ack, 5*time.Second)

	if err := eth0.RequestBodies(msg); err != nil {
		t.Fatal(err)
	}

	resp := <-ack
	if !resp.Complete {
		t.Fatal("not completed")
	}
	var bodies []*types.Body
	if err := rlp.Decode(resp.Payload, &bodies); err != nil {
		t.Fatal(err)
	}
	if len(bodies) != len(batch) {
		t.Fatal("bodies: length is not correct")
	}
	for indx := range batch {
		if batch[indx] != bodies[indx].Transactions[0].Nonce() {
			t.Fatal("numbers dont match")
		}
	}

	// -- receipts --

	ack = make(chan network.AckMessage, 1)
	eth0.Conn().SetHandler(ReceiptsMsg, ack, 5*time.Second)

	if err := eth0.RequestReceipts(msg); err != nil {
		t.Fatal(err)
	}

	resp = <-ack
	if !resp.Complete {
		t.Fatal("not completed")
	}
	var receiptsResp [][]*types.Receipt
	if err := rlp.Decode(resp.Payload, &receiptsResp); err != nil {
		t.Fatal(err)
	}
	if len(receiptsResp) != len(batch) {
		t.Fatal("receipts: length is not correct")
	}
	for indx, i := range batch {
		// cumulativegasused is the index of the block to which the receipt belongs
		if i != receiptsResp[indx][0].CumulativeGasUsed {
			t.Fatal("error")
		}
	}
}

func TestPeerConcurrentHeaderCalls(t *testing.T) {
	headers := blockchain.NewTestHeaderChain(1000)

	// b0 with only the genesis
	b0 := blockchain.NewTestBlockchain(t, headers[0:5])

	// b1 with the whole chain
	b1 := blockchain.NewTestBlockchain(t, headers)

	s0, s1 := network.TestServers()
	p0, _ := testEthHandshake(t, s0, &status, b0, s1, &status, b1)

	cases := []uint64{10}
	errr := make(chan error, len(cases))

	for indx, i := range cases {
		go func(indx int, i uint64) {
			h, err := p0.RequestHeadersSync(i, 100)
			if err == nil {
				if len(h) != 100 {
					err = fmt.Errorf("length not correct")
				} else {
					for indx, j := range h {
						if j.Number.Uint64() != i+uint64(indx) {
							err = fmt.Errorf("numbers dont match")
							break
						}
					}
				}
			}
			errr <- err
		}(indx, i)
	}

	for i := 0; i < len(cases); i++ {
		if err := <-errr; err != nil {
			t.Fatal(err)
		}
	}
}

func TestPeerEmptyResponseFails(t *testing.T) {
	headers := blockchain.NewTestHeaderChain(1000)

	// b0 with only the genesis
	b0 := blockchain.NewTestBlockchain(t, headers[0:5])

	// b1 with the whole chain
	b1 := blockchain.NewTestBlockchain(t, headers)

	s0, s1 := network.TestServers()
	p0, _ := testEthHandshake(t, s0, &status, b0, s1, &status, b1)

	if _, err := p0.RequestHeadersSync(1100, 100); err == nil {
		t.Fatal("it should fail")
	}

	// NOTE: We cannot know from an empty response which is the
	// pending block it belongs to (because we use the first block to know the origin)
	// Thus, the query will fail with a timeout message but it does not
	// mean the peer is timing out on the responses.
}

func TestPeerCloseConnection(t *testing.T) {
	// close the connection while doing the request

	headers := blockchain.NewTestHeaderChain(1000)

	// b0 with only the genesis
	b0 := blockchain.NewTestBlockchain(t, headers[0:5])

	// b1 with the whole chain
	b1 := blockchain.NewTestBlockchain(t, headers)

	s0, s1 := network.TestServers()
	p0, _ := testEthHandshake(t, s0, &status, b0, s1, &status, b1)

	if _, err := p0.RequestHeadersSync(0, 100); err != nil {
		t.Fatal(err)
	}

	s1.Close()
	if _, err := p0.RequestHeadersSync(100, 100); err == nil {
		t.Fatal("it should fail after the connection has been closed")
	}
}
