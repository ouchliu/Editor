package triblab

import (
	//	"fmt"
	"net/rpc"
	"sync"
	. "trib"
)

type client struct {
	addr string
	srv  *rpc.Client
	lock sync.Mutex
}

func (self *client) connect(firsttime bool) error {
	self.lock.Lock()
	defer self.lock.Unlock()
	if (self.srv != nil) && firsttime {
		return nil
	}
	var err error
	for i := 0; i < 3; i++ {
		self.srv, err = rpc.DialHTTP("tcp", self.addr)
		if err == nil {
			return nil
		}
	}
	return err
}

func (self *client) Get(key string, value *string) error {
	if e := self.connect(true); e != nil {
		return e
	}

	for e := self.srv.Call("Storage.Get", key, value); e != nil; e = self.srv.Call("Storage.Get", key, value){
		if e = self.connect(false); e != nil {
			return e
		}
	}
	return nil
}

func (self *client) Set(kv *KeyValue, succ *bool) error {
	if e := self.connect(true); e != nil {
		return e
	}

	for e := self.srv.Call("Storage.Set", kv, succ); e != nil; e = self.srv.Call("Storage.Set", kv, succ){
		if e = self.connect(false); e != nil {
			return e
		}
	}
	return nil
}

func (self *client) Keys(p *Pattern, list *List) error {
	if e := self.connect(true); e != nil {
		return e
	}
	list.L = make([]string, 0)
	for e := self.srv.Call("Storage.Keys", p, list); e != nil; e = self.srv.Call("Storage.Keys", p, list){
		if e = self.connect(false); e != nil {
			return e
		}
	}
	return nil
}

func (self *client) ListGet(key string, list *List) error {
	if e := self.connect(true); e != nil {
		return e
	}
	list.L = make([]string, 0)
	for e := self.srv.Call("Storage.ListGet", key, list); e != nil; e = self.srv.Call("Storage.ListGet", key, list){
		if e = self.connect(false); e != nil {
			return e
		}
	}
	return nil
}

func (self *client) ListAppend(kv *KeyValue, succ *bool) error {
	if e := self.connect(true); e != nil {
		return e
	}

	for e := self.srv.Call("Storage.ListAppend", kv, succ); e != nil; e = self.srv.Call("Storage.ListAppend", kv, succ){
		if e = self.connect(false); e != nil {
			return e
		}
	}
	return nil
}

func (self *client) ListRemove(kv *KeyValue, n *int) error {
	if e := self.connect(true); e != nil {
		return e
	}

	for e := self.srv.Call("Storage.ListRemove", kv, n); e != nil; e = self.srv.Call("Storage.ListRemove", kv, n){
		if e = self.connect(false); e != nil {
			return e
		}
	}
	return nil
}

func (self *client) ListKeys(p *Pattern, list *List) error {
	if e := self.connect(true); e != nil {
		return e
	}
	list.L = make([]string, 0)
	for e := self.srv.Call("Storage.ListKeys", p, list); e != nil; e = self.srv.Call("Storage.ListKeys", p, list){
		if e = self.connect(false); e != nil {
			return e
		}
	}
	return nil
}

func (self *client) Clock(atLeast uint64, ret *uint64) error {
	if e := self.connect(true); e != nil {
		return e
	}

	for e := self.srv.Call("Storage.Clock", atLeast, ret); e != nil; e = self.srv.Call("Storage.Clock", atLeast, ret){
		if e = self.connect(false); e != nil {
			return e
		}
	}
	return nil
}

var _ Storage = new(client)
