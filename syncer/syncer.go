package syncer

import (
	"fmt"
	"math"
	"math/big"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/umbracle/minimal/network"
	"github.com/umbracle/minimal/protocol/ethereum"
)

type Config struct {
	MaxRequests int
	NumWorkers  int
}

func DefaultConfig() *Config {
	c := &Config{
		MaxRequests: 5,
		NumWorkers:  2,
	}
	return c
}

// Blockchain is the reference the syncer needs to connect to the blockchain
type Blockchain interface {
	Header() *types.Header
	Genesis() *types.Header
	WriteHeaders(headers []*types.Header) error
	GetHeaderByNumber(number *big.Int) *types.Header
}

type Peer struct {
	pretty  string
	id      string
	conn    *ethereum.Ethereum
	peer    *network.Peer
	active  bool
	failed  int
	pending int
}

func newPeer(id string, peer *network.Peer) *Peer {
	return &Peer{
		id:     id,
		active: true,
		pretty: peer.PrettyString(),
		conn:   peer.GetProtocol("eth", 63).(*ethereum.Ethereum),
		peer:   peer,
	}
}

// Syncer is the syncer protocol
type Syncer struct {
	NetworkID uint64
	config    *Config

	blockchain Blockchain
	queue      *queue

	counter int
	last    int

	peers     map[string]*Peer
	peersLock sync.Mutex
	waitCh    []chan struct{}

	deliverLock sync.Mutex
}

// NewSyncer creates a new syncer
func NewSyncer(networkID uint64, blockchain Blockchain, config *Config) (*Syncer, error) {
	s := &Syncer{
		config:      config,
		NetworkID:   networkID,
		peers:       map[string]*Peer{},
		peersLock:   sync.Mutex{},
		blockchain:  blockchain,
		queue:       newQueue(),
		counter:     0,
		last:        0,
		deliverLock: sync.Mutex{},
		waitCh:      make([]chan struct{}, 0),
	}

	header := blockchain.Header()

	s.queue.front = s.queue.newItem(header.Number.Uint64() + 1)
	s.queue.head = header.Hash()

	// Maybe start s.back as s.front and calls to dequeue would block

	fmt.Printf("Current header (%d): %s\n", header.Number.Uint64(), header.Hash().String())

	return s, nil
}

func (s *Syncer) updateChain(block uint64) {
	// updates the back object
	if s.queue.back == nil {
		s.queue.addBack(block)
	} else {
		if block > s.queue.back.block {
			s.queue.addBack(block)
		}
	}
}

// AddNode is called when we connect to a new node
func (s *Syncer) AddNode(peer *network.Peer) {
	fmt.Println("----- ADD NODE -----")

	if err := s.checkDAOHardFork(peer.GetProtocol("eth", 63).(*ethereum.Ethereum)); err != nil {
		fmt.Println("Failed to check the DAO block")
		return
	}

	fmt.Println("DAO Fork completed")

	p := newPeer(peer.ID, peer)
	s.peers[peer.ID] = p

	// find data about the peer
	header, err := s.fetchHeight(p.conn)
	if err != nil {
		fmt.Printf("ERR: fetch height failed: %v\n", err)
		return
	}

	/*
		ancestor, err := p.FindCommonAncestor()
		if err != nil {
			fmt.Printf("ERR: ancestor failed: %v\n", err)
		}
	*/

	fmt.Printf("Heigth: %d\n", header.Number.Uint64())
	// fmt.Printf("Ancestor: %d\n", ancestor.Number.Uint64())

	// check that the difficulty is higher than ours
	if peer.HeaderDiff().Cmp(s.blockchain.Header().Difficulty) < 0 {
		fmt.Printf("Difficulty %s is lower than ours %s, skip it\n", peer.HeaderDiff().String(), s.blockchain.Header().Difficulty.String())
	}

	fmt.Println("Difficulty higher than ours")
	s.updateChain(header.Number.Uint64())

	// wake up some task
	s.wakeUp()
	// go s.runPeer(p)
}

func (s *Syncer) runPeer(peer *Peer) {
	/*
		block := 0

		for {
			fmt.Printf("Peer (%s) %d\n", peer.pretty, block)

			headers, err := peer.conn.RequestHeadersSync(uint64(block), 100)
			if err != nil {
				fmt.Printf("failed to deliver: %v", err)
				return
			}

			bodies := []*types.Header{}
			receipts := []*types.Header{}

			for _, h := range headers {
				if hasBody(h) {
					bodies = append(bodies, h)
				}
				if hasReceipts(h) {
					receipts = append(receipts, h)
				}
			}

			if _, err := peer.conn.RequestBodiesSync(bodies); err != nil {
				fmt.Printf("failed to receive receipts: %v", err)
			}

			if _, err := peer.conn.RequestReceiptsSync(receipts); err != nil {
				fmt.Printf("failed to receive bodies: %v", err)
			}

			block += 100
		}
	*/
}

func (s *Syncer) workerTask(id string) {
	fmt.Printf("Worker task starts: %s\n", id)

	for {
		peer := s.dequeuePeer()

		fmt.Printf("(%s) peer is ready %s: %d\n", id, peer.pretty, peer.pending)

		i := s.dequeue()

		var data interface{}
		var err error
		var context string

		switch job := i.payload.(type) {
		case *HeadersJob:
			fmt.Printf("SYNC HEADERS (%s) (%d) (%s): %d, %d\n", id, i.id, peer.pretty, job.block, job.count)

			data, err = peer.conn.RequestHeadersSync(job.block, job.count)
			context = "headers"
		case *BodiesJob:
			fmt.Printf("SYNC BODIES (%s) (%d) (%s): %d\n", id, i.id, peer.pretty, len(job.hashes))

			data, err = peer.conn.RequestBodiesSync(job.hash, job.hashes)
			context = "bodies"
		case *ReceiptsJob:
			fmt.Printf("SYNC RECEIPTS (%s) (%d) (%s): %d\n", id, i.id, peer.pretty, len(job.hashes))

			data, err = peer.conn.RequestReceiptsSync(job.hash, job.hashes)
			context = "receipts"
		}

		// ack the job and enqueue again for more work
		s.ack(peer, err != nil)

		s.deliver(peer.pretty, context, i.id, data, err)

		fmt.Printf("Completed: %d\n", s.queue.NumOfCompletedBatches())
	}
}

// Run is the main entry point
func (s *Syncer) Run() {
	for i := 0; i < s.config.NumWorkers; i++ {
		go s.workerTask(strconv.Itoa(i))
	}
}

type peers []*Peer

func (p peers) Len() int {
	return len(p)
}
func (p peers) Swap(i, j int) {
	p[i], p[j] = p[j], p[i]
}
func (p peers) Less(i, j int) bool {
	if p[i].failed < p[j].failed {
		return true
	}
	return p[i].pending < p[j].pending
}

func (s *Syncer) dequeuePeer() *Peer {
SELECT:
	//fmt.Println("-- getting peers --")
	s.peersLock.Lock()
	pp := peers{}
	for _, i := range s.peers {
		//fmt.Printf("Peer: %s %v %d %v\n", i.pretty, i.active, i.pending, i.pending <= s.config.MaxRequests-1)
		if i.active && i.pending <= s.config.MaxRequests-1 {
			pp = append(pp, i)
		}
	}
	sort.Sort(pp)

	//fmt.Println("-- pending --")
	//fmt.Println(pp)

	if len(pp) > 0 {
		candidate := pp[0]
		candidate.pending++

		s.peersLock.Unlock()
		return candidate
	}

	s.peersLock.Unlock()

	fmt.Println("- nothign found go to sleep -")

	// wait and sleep
	wait := make(chan struct{})
	s.waitCh = append(s.waitCh, wait)

	for {
		<-wait
		fmt.Println("- awake now -")
		goto SELECT
	}
}

func (s *Syncer) ack(peer *Peer, failed bool) {
	s.peersLock.Lock()

	// acknoledge a task has finished
	peer.pending--

	if failed {
		peer.failed++
	}
	if peer.failed == 10 {
		peer.active = false
	}
	s.peersLock.Unlock()

	if peer.active {
		for i := peer.pending; i < s.config.MaxRequests; i++ {
			s.wakeUp()
		}
	}
}

func (s *Syncer) wakeUp() {
	//fmt.Println("- wake up -")
	//fmt.Println(len(s.waitCh))

	// wake up a task if necessary
	if len(s.waitCh) > 0 {
		var wake chan struct{}
		wake, s.waitCh = s.waitCh[0], s.waitCh[1:]

		select {
		case wake <- struct{}{}:
		default:
		}
	}
}

func (s *Syncer) dequeue() *Job {
	job, err := s.queue.Dequeue()
	if err != nil {
		fmt.Printf("Failed to dequeue: %v\n", err)
	}
	return job
}

func (s *Syncer) deliver(peer string, context string, id uint32, data interface{}, err error) {
	s.deliverLock.Lock()
	defer s.deliverLock.Unlock()

	if err != nil {
		// log
		// TODO, we need to set here the thing that was not deliver as waiting to be dequeued again
		fmt.Printf("==================================> Failed to deliver (%d): %v\n", id, err)
		if err := s.queue.updateFailedElem(peer, id, context); err != nil {
			fmt.Printf("Could not be updated: %v\n", err)
		}
		return
	}

	switch obj := data.(type) {
	case []*types.Header:
		fmt.Printf("deliver headers %d: %d\n", id, len(obj))
		if err := s.queue.deliverHeaders(id, obj); err != nil {
			fmt.Printf("Failed to deliver headers (%d): %v\n", id, err)

			panic("")
		}

	case []*types.Body:
		fmt.Printf("deliver bodies %d: %d\n", id, len(obj))
		if err := s.queue.deliverBodies(id, obj); err != nil {
			fmt.Printf("Failed to deliver bodies (%d): %v\n", id, err)

			panic("")
		}

	case [][]*types.Receipt:
		fmt.Printf("deliver receipts %d: %d\n", id, len(obj))
		if err := s.queue.deliverReceipts(id, obj); err != nil {
			fmt.Printf("Failed to deliver receipts (%d): %v\n", id, err)

			panic("")
		}

	default:
		panic(data)
	}

	fmt.Println(s.queue.NumOfCompletedBatches())
	if n := s.queue.NumOfCompletedBatches(); n == s.last {
		s.counter++
	} else {
		s.last = n
		s.counter = 0
	}

	if s.counter == 500 {
		s.queue.printQueue()
		panic("")
	}

	if s.queue.NumOfCompletedBatches() > 2 {
		data := s.queue.FetchCompletedData()

		fmt.Printf("Commit data: %d\n", len(data)*maxElements)
		fmt.Printf("New Head: %s\n", s.queue.head.String())

		// write the headers
		for indx, elem := range data {
			if err := s.blockchain.WriteHeaders(elem.headers); err != nil {
				fmt.Printf("Failed to write headers batch: %v", err)

				first, last := elem.headers[0], elem.headers[len(elem.headers)-1]

				fmt.Printf("Error at step: %d\n", indx)
				fmt.Printf("First block we have is %s (%s) %s (%s)\n", first.Hash().String(), first.Number.String(), last.Hash().String(), last.Number.String())

				panic("")
				return
			}
		}
	}
}

// GetStatus returns the current ethereum status
func (s *Syncer) GetStatus() (*ethereum.Status, error) {
	header := s.blockchain.Header()

	status := &ethereum.Status{
		ProtocolVersion: 63,
		NetworkID:       s.NetworkID,
		TD:              header.Difficulty,
		CurrentBlock:    header.Hash(),
		GenesisBlock:    s.blockchain.Genesis().Hash(),
	}
	return status, nil
}

var (
	daoBlock            = uint64(1920000)
	daoChallengeTimeout = 5 * time.Second
)

func (s *Syncer) checkDAOHardFork(eth *ethereum.Ethereum) error {
	return nil // hack

	if s.NetworkID == 1 {
		ack := make(chan network.AckMessage, 1)
		eth.Conn().SetHandler(ethereum.BlockHeadersMsg, ack, daoChallengeTimeout)

		// check the DAO block
		if err := eth.RequestHeadersByNumber(daoBlock, 1, 0, false); err != nil {
			return err
		}

		resp := <-ack
		if resp.Complete {
			var headers []*types.Header
			if err := rlp.Decode(resp.Payload, &headers); err != nil {
				return err
			}

			// TODO. check that daoblock is correct
			fmt.Println(headers)

		} else {
			return fmt.Errorf("timeout")
		}
	}

	return nil
}

// FindCommonAncestor finds the common ancestor with the peer and the syncer connection
func (s *Syncer) FindCommonAncestor(peer *ethereum.Ethereum) (*types.Header, error) {
	// Binary search, TODO, works but it may take a lot of time

	min := 0 // genesis
	max := int(s.blockchain.Header().Number.Uint64())

	height, err := s.fetchHeight(peer)
	if err != nil {
		return nil, err
	}
	if heightNumber := int(height.Number.Uint64()); max > heightNumber {
		max = heightNumber
	}

	var header *types.Header

	for min <= max {
		m := uint64(math.Floor(float64(min+max) / 2))

		headers, err := peer.RequestHeadersSync(m, 1)
		if err != nil {
			return nil, err
		}

		l := len(headers)
		if l == 0 {
			// peer does not have the m peer, search in lower bounds
			max = int(m - 1)
		} else if l == 1 {
			header = headers[0]
			if header.Number.Uint64() != m {
				return nil, fmt.Errorf("header response number not correct, asked %d but retrieved %d", m, header.Number.Uint64())
			}

			expectedHeader := s.blockchain.GetHeaderByNumber(big.NewInt(int64(m)))
			if expectedHeader == nil {
				return nil, fmt.Errorf("cannot find the header in local chain")
			}

			if expectedHeader.Hash() == header.Hash() {
				min = int(m + 1)
			} else {
				max = int(m - 1)
			}
		} else {
			return nil, fmt.Errorf("expected either 1 or 0 headers")
		}
	}

	if min == 0 {
		return nil, nil
	}
	return header, nil
}

// fetchHeight returns the header of the head hash of the peer
func (s *Syncer) fetchHeight(peer *ethereum.Ethereum) (*types.Header, error) {
	head := peer.Header()

	ack := make(chan network.AckMessage, 1)
	peer.Conn().SetHandler(ethereum.BlockHeadersMsg, ack, 30*time.Second)

	if err := peer.RequestHeadersByHash(head, 1, 0, false); err != nil {
		return nil, err
	}

	resp := <-ack
	if !resp.Complete {
		return nil, fmt.Errorf("timeout")
	}

	var headers []*types.Header
	if err := rlp.Decode(resp.Payload, &headers); err != nil {
		return nil, err
	}
	if len(headers) != 1 {
		return nil, fmt.Errorf("expected one but found %d", len(headers))
	}

	header := headers[0]
	if header.Hash() != head {
		return nil, fmt.Errorf("returned hash is not the correct one")
	}

	return header, nil
}
