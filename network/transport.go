package network

import (
	"fmt"
	"log"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/oxtoacart/bpool"

	"github.com/armon/go-metrics"
	"github.com/umbracle/minimal/network/discover"
)

// Transport is used to abstract over communicating with other peers. The packet
// interface is assumed to be best-effort and the stream interface is assumed to
// be reliable.
type Transport interface {
	// Discover UDP transport
	discover.Transport

	// DialTimeout is used to create a connection that allows us to perform
	// two-way communication with a peer. This is generally more expensive
	// than packet connections so is used for more infrequent operations
	// such as anti-entropy or fallback probes if the packet-oriented probe
	// failed.
	DialTimeout(addr string, timeout time.Duration) (net.Conn, error)

	// StreamCh returns a channel that can be read to handle incoming stream
	// connections from other peers. How this is set up for listening is
	// left as an exercise for the concrete transport implementations.
	StreamCh() <-chan net.Conn

	// Shutdown is called when memberlist is shutting down; this gives the
	// transport a chance to clean up any listeners.
	Shutdown() error
}

const (
	// udpPacketBufSize is used to buffer incoming packets during read
	// operations.
	udpPacketBufSize = 1280

	// udpRecvBufSize is a large buffer size that we attempt to set UDP
	// sockets to in order to handle a large volume of messages.
	udpRecvBufSize = 2 * 1024 * 1024
)

// NetTransportConfig is used to configure a net transport.
type NetTransportConfig struct {
	// BindAddrs is a list of addresses to bind to for both TCP and UDP
	// communications.
	BindAddrs []string

	// BindPort is the port to listen on, for each address above.
	BindPort int

	// Logger is a logger for operator messages.
	Logger *log.Logger
}

// NetTransport is a Transport implementation that uses connectionless UDP for
// packet operations, and ad-hoc TCP connections for stream operations.
type NetTransport struct {
	config       *NetTransportConfig
	packetCh     chan *discover.Packet
	streamCh     chan net.Conn
	logger       *log.Logger
	wg           sync.WaitGroup
	tcpListeners []*net.TCPListener
	udpListeners []*net.UDPConn
	shutdown     int32
	buffer       *bpool.BytePool
}

// NewNetTransport returns a net transport with the given configuration. On
// success all the network listeners will be created and listening.
func NewNetTransport(config *NetTransportConfig) (*NetTransport, error) {
	// If we reject the empty list outright we can assume that there's at
	// least one listener of each type later during operation.
	if len(config.BindAddrs) == 0 {
		return nil, fmt.Errorf("At least one bind address is required")
	}

	// Build out the new transport.
	var ok bool
	t := NetTransport{
		config:   config,
		packetCh: make(chan *discover.Packet),
		streamCh: make(chan net.Conn),
		logger:   config.Logger,
		buffer:   bpool.NewBytePool(10, udpPacketBufSize),
	}

	// Clean up listeners if there's an error.
	defer func() {
		if !ok {
			t.Shutdown()
		}
	}()

	// Build all the TCP and UDP listeners.
	port := config.BindPort
	for _, addr := range config.BindAddrs {
		ip := net.ParseIP(addr)

		tcpAddr := &net.TCPAddr{IP: ip, Port: port}
		tcpLn, err := net.ListenTCP("tcp", tcpAddr)
		if err != nil {
			return nil, fmt.Errorf("Failed to start TCP listener on %q port %d: %v", addr, port, err)
		}
		t.tcpListeners = append(t.tcpListeners, tcpLn)

		// If the config port given was zero, use the first TCP listener
		// to pick an available port and then apply that to everything
		// else.
		if port == 0 {
			port = tcpLn.Addr().(*net.TCPAddr).Port
		}

		udpAddr := &net.UDPAddr{IP: ip, Port: port}
		udpLn, err := net.ListenUDP("udp", udpAddr)
		if err != nil {
			return nil, fmt.Errorf("Failed to start UDP listener on %q port %d: %v", addr, port, err)
		}
		/*
			if err := setUDPRecvBuf(udpLn); err != nil {
				return nil, fmt.Errorf("Failed to resize UDP buffer: %v", err)
			}
		*/
		t.udpListeners = append(t.udpListeners, udpLn)
	}

	// Fire them up now that we've been able to create them all.
	for i := 0; i < len(config.BindAddrs); i++ {
		t.wg.Add(2)
		go t.tcpListen(t.tcpListeners[i])
		go t.udpListen(t.udpListeners[i])
	}

	ok = true
	return &t, nil
}

// See Transport.
func (t *NetTransport) WriteTo(b []byte, addr string) (time.Time, error) {
	udpAddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return time.Time{}, err
	}

	// We made sure there's at least one UDP listener, so just use the
	// packet sending interface on the first one. Take the time after the
	// write call comes back, which will underestimate the time a little,
	// but help account for any delays before the write occurs.
	_, err = t.udpListeners[0].WriteTo(b, udpAddr)
	return time.Now(), err
}

// See Transport.
func (t *NetTransport) PacketCh() <-chan *discover.Packet {
	return t.packetCh
}

// See Transport.
func (t *NetTransport) DialTimeout(addr string, timeout time.Duration) (net.Conn, error) {
	dialer := net.Dialer{Timeout: timeout}
	return dialer.Dial("tcp", addr)
}

// See Transport.
func (t *NetTransport) StreamCh() <-chan net.Conn {
	return t.streamCh
}

// See Transport.
func (t *NetTransport) Shutdown() error {
	// This will avoid log spam about errors when we shut down.
	atomic.StoreInt32(&t.shutdown, 1)

	// Rip through all the connections and shut them down.
	for _, conn := range t.tcpListeners {
		conn.Close()
	}
	for _, conn := range t.udpListeners {
		conn.Close()
	}

	// Block until all the listener threads have died.
	t.wg.Wait()
	return nil
}

// tcpListen is a long running goroutine that accepts incoming TCP connections
// and hands them off to the stream channel.
func (t *NetTransport) tcpListen(tcpLn *net.TCPListener) {
	defer t.wg.Done()
	for {
		conn, err := tcpLn.AcceptTCP()
		if err != nil {
			if s := atomic.LoadInt32(&t.shutdown); s == 1 {
				break
			}

			t.logger.Printf("[ERR] minimal: Error accepting TCP connection: %v", err)
			continue
		}

		t.streamCh <- conn
	}
}

// udpListen is a long running goroutine that accepts incoming UDP packets and
// hands them off to the packet channel.
func (t *NetTransport) udpListen(udpLn *net.UDPConn) {
	defer t.wg.Done()
	for {
		// Do a blocking read into a fresh buffer. Grab a time stamp as
		// close as possible to the I/O.
		// buf := make([]byte, udpPacketBufSize)
		buf := t.buffer.Get()
		n, addr, err := udpLn.ReadFrom(buf)
		ts := time.Now()
		if err != nil {
			if s := atomic.LoadInt32(&t.shutdown); s == 1 {
				break
			}

			t.logger.Printf("[ERR] minimal: Error reading UDP packet: %v", err)
			continue
		}

		// Check the length - it needs to have at least one byte to be a
		// proper message.
		if n < 1 {
			t.logger.Printf("[ERR] minimal: UDP packet too short (%d bytes) %s",
				len(buf), addr)
			continue
		}

		// Ingest the packet.
		metrics.IncrCounter([]string{"minimal", "udp", "received"}, float32(n))
		t.packetCh <- &discover.Packet{
			Buf:       buf[:n],
			From:      addr,
			Timestamp: ts,
			Release: func() {
				t.buffer.Put(buf)
			},
		}
	}
}

// setUDPRecvBuf is used to resize the UDP receive window. The function
// attempts to set the read buffer to `udpRecvBuf` but backs off until
// the read buffer can be set.
func setUDPRecvBuf(c *net.UDPConn) error {
	size := udpRecvBufSize
	var err error
	for size > 0 {
		if err = c.SetReadBuffer(size); err == nil {
			return nil
		}
		size = size / 2
	}
	return err
}
