package triblab

import (
	//	"fmt"
	"net"
	"net/http"
	"net/rpc"
	"trib"
)

// Creates an RPC client that connects to addr.
func NewClient(addr string) trib.Storage {
	return &client{addr: addr}
}

// Serve as a backend based on the given configuration
func ServeBack(b *trib.BackConfig) error {
	lsn, e := net.Listen("tcp", b.Addr)
	if e != nil {
		//		fmt.Println("Cannot bind to the address " + b.Addr + "!")
		if b.Ready != nil {
			b.Ready <- false
		}
		return e
	}
	rpcserver := rpc.NewServer()
	e = rpcserver.Register(b.Store)
	if e != nil {
		return e
		if b.Ready != nil {
			b.Ready <- false
		}
	}
	if b.Ready != nil {
		b.Ready <- true
	}
	return http.Serve(lsn, rpcserver)
}
