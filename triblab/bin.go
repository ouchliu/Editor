package triblab

import (
	"crypto/sha1"
	"encoding/base64"
	//	"fmt"
	"hash"
	"io"
	"sort"
	"sync"
	"time"
	. "trib"
	"trib/colon"
)

type bin struct {
	backs        []string //all possible backend addresses
	available    []string //backends that are alive
	hashfunction hash.Hash
	binclients   map[string]Storage //cache for each bin
	clients      map[string]Storage //reuse connections for each backends
	lock         sync.Mutex
}

func (self *bin) Init() {
	self.hashfunction = sha1.New()
	self.available = make([]string, len(self.backs))
	for i, v := range self.backs {
		self.available[i] = getHash(self.hashfunction, v)
	}
	sort.Strings(self.available)
	self.clients = make(map[string]Storage)
	self.binclients = make(map[string]Storage)
	ch := make(chan bool, len(self.backs))
	//initialize available address
	a := make([]string, 0)
	for _, v := range self.backs {
		go func(addr string) {
			cl := NewClient(addr)
			hashaddr := getHash(self.hashfunction, addr)
			self.clients[hashaddr] = cl
			var t uint64
			if e := cl.Clock(uint64(0), &t); e == nil {
				self.lock.Lock()
				defer self.lock.Unlock()
				a = append(a, hashaddr)
				ch <- true
			} else {
				ch <- false
			}
		}(v)
	}
	counter := 0
	for i := 0; i < len(self.backs); i++ {
		if <-ch {
			counter++
		}
	}
	sort.Strings(a)
	self.available = a
	//Wait for the keepers to initialize the backs
	//time.Sleep(100 * time.Millisecond)
	go self.daemon()
}

//daemon thread update available backends every 0.1s
func (self *bin) daemon() {
	counter := 0
	tick := time.Tick(100 * time.Millisecond)
	//The available backends are stored with prefix "BACK" in each backends.
	p := Pattern{Prefix: "BACK", Suffix: ""}
	for {
		select {
		case <-tick:
			ls := List{}
			//retrive available backend list from backend.
			cl := self.clients[self.available[counter%len(self.available)]]
			counter++
			//Only one backend can go down each time
			if cl.Keys(&p, &ls) != nil {
				cl = self.clients[self.available[counter%len(self.available)]]
				counter++
				cl.Keys(&p, &ls)
			}
			if len(ls.L) > 0{
				l := ls.L[:]
				for i, v := range l {
					l[i] = v[4:]
				}
				sort.Strings(l)
				//Update available list
				self.available = l
			}
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
}

//generate clients to backends from current hashs informathion
func (self *bin) getclients(hashname string) ([]Storage, []string) {
	available := self.available[:]
	nhash := len(available)
	baseidx := sort.SearchStrings(available, hashname) % nhash
	duplica1 := (baseidx + 1) % nhash
	duplica2 := (baseidx + 2) % nhash
	backup := (baseidx + 3) % nhash
	result := make([]Storage, 4)
	hashs := make([]string, 4)
	hashs[0] = available[baseidx]
	hashs[1] = available[duplica1]
	hashs[2] = available[duplica2]
	hashs[3] = available[backup]
	result[0] = self.clients[hashs[0]]
	result[1] = self.clients[hashs[1]]
	result[2] = self.clients[hashs[2]]
	result[3] = self.clients[hashs[3]]
	/*
		for _, r := range result{
			fmt.Println(r)
		}
	*/
	return result, hashs
}

//Get the from hash value of the backend
func (self *bin) getfrom(hashaddr string) (string, error) {
	var res string
	if e := self.clients[hashaddr].Get("FROM", &res); e != nil {
		return "", e
	}
	return res, nil
}

func (self *bin) Bin(word string) Storage {
	if cl, found := self.binclients[word]; found {
		return cl
	}
	cl := self.bin(word)
	self.binclients[word] = cl
	return cl
}

func (self *bin) bin(word string) Storage {
	hashword := getHash(self.hashfunction, word)
	return &binclient{
		bs:       self,
		name:     word,
		hashname: hashword,
		prefix:   colon.Escape(word) + "::",
		prelen:   len(colon.Escape(word)) + 2,
	}
}

func getHash(hasher hash.Hash, in string) string {
	hasher.Reset()
	io.WriteString(hasher, in)
	bytes := hasher.Sum(nil)
	return base64.URLEncoding.EncodeToString(bytes)
}

type binclient struct {
	bs       *bin   //the bin issued this instance
	name     string //bin name
	prefix   string //prefix, used in encoding
	prelen   int    //length of prefix
	hashname string //hash value of bin name
}

func (self *binclient) encode(key string) string {
	return self.prefix + colon.Escape(key)
}

func (self *binclient) decode(key string) string {
	return colon.Unescape(key[self.prelen:])
}

func Deduplicate(keys []string) []string {
	m := make(map[string]bool)
	for _, v := range keys {
		m[v] = true
	}
	result := make([]string, len(m))
	counter := 0
	for k, _ := range m {
		result[counter] = k
		counter++
	}
	return result
}

func (self *binclient) Get(key string, value *string) error {
	cls, hashaddrs := self.bs.getclients(self.hashname)
	//KV is for log of KV pair ops
	logkey := "KV" + self.encode(key)
	ls := List{}
	var e error
	var from string
	//Check the availability of the master
	from, e = self.bs.getfrom(hashaddrs[0])
	//Guarenteed to succeed during recovery mode
	if e != nil || from == hashaddrs[0] {
		if e = cls[1].ListGet(logkey, &ls); e != nil {
			return e
		}
	} else if cls[0].ListGet(logkey, &ls) != nil {
		//Backend leave
		if e = cls[1].ListGet(logkey, &ls); e != nil {
			return e
		}
	}
	//That is, no result exist.
	if len(ls.L) == 0 {
		*value = ""
		return nil
	}
	var log LogEvent
	res := ParseString(ls.L[0])
	//Find the latest log and use the value
	for _, v := range ls.L {
		log = ParseString(v)
		if log.Clock > res.Clock {
			res = log
		}
	}
	*value = res.KV.Value
	return nil
}

func (self *binclient) writelog(logkv KeyValue, cls []Storage) error {
	var b bool
	ch := make(chan error, 3)
	defer close(ch)
	allow := 1
	nerror := 0
	var err error
	if len(cls) == 3 {
		allow = 0
	}
	//Append log to three copies
	for i := 0; i < 3; i++ {
		go func(idx int) {
			ch <- cls[idx].ListAppend(&logkv, &b)
		}(i)
	}
	for i := 0; i < 3; i++ {
		if e := <-ch; e != nil {
			err = e
			nerror++
		}
	}
	if nerror > allow {
		return err
	}
	//At most 1 error may happen
	if nerror > 0 {
		return cls[3].ListAppend(&logkv, &b)
	}
	return nil
}

func (self *binclient) Set(kv *KeyValue, succ *bool) error {
	cls, hashaddrs := self.bs.getclients(self.hashname)
	var e error
	var from string
	//Check the availability of the master
	if from, e = self.bs.getfrom(hashaddrs[0]); e == nil {
		if from < hashaddrs[0] && self.hashname <= from || from > hashaddrs[0] && self.hashname >= hashaddrs[0] && self.hashname < from {
			//Master joined or just left
			cls = append([]Storage{self.bs.clients[from]}, cls...)
		} else {
			if from, e = self.bs.getfrom(hashaddrs[1]); e == nil {
				//Maybe duplica back or leave
				if hashaddrs[0] < hashaddrs[1] && from > hashaddrs[0] && from < hashaddrs[1] || hashaddrs[0] > hashaddrs[1] && (from > hashaddrs[0] || from < hashaddrs[1]) {
					cls = append([]Storage{self.bs.clients[from]}, cls...)
				} else {
					if from, e = self.bs.getfrom(hashaddrs[2]); e == nil {
						//Maybe duplica back or leave
						if hashaddrs[1] < hashaddrs[2] && from > hashaddrs[1] && from < hashaddrs[2] || hashaddrs[1] > hashaddrs[2] && (from > hashaddrs[1] || from < hashaddrs[2]) {
							cls = append([]Storage{self.bs.clients[from]}, cls...)
						}
					} else {
						//Duplica 2 failure
						cls = append(cls[:2], cls[3])
					}
				}
			} else {
				//Duplica 1 failure
				cls = append(cls[:1], cls[2:]...)
			}
		}
	} else {
		//master failure
		cls = cls[1:]
	}
	kv.Key = self.encode(kv.Key)
	var t uint64
	//Get clock
	if e = self.Clock(uint64(0), &t); e != nil {
		return e
	}
	log := LogEvent{Clock: t, KV: *kv, CMD: "S"}
	logkv := KeyValue{Key: "KV" + kv.Key, Value: log.toString()}
	//Write log to backends
	if e = self.writelog(logkv, cls); e != nil {
		*succ = false
		return e
	}
	*succ = true
	return nil
}

/*
 *  Check backend authority, find the correct backend, download all KV log entries,
 *  download all KV logs, find all keys that are not empty. Don't forget decode key
 */
func (self *binclient) Keys(p *Pattern, list *List) error {
	list.L = make([]string, 0)
	cls, hashaddrs := self.bs.getclients(self.hashname)
	//KV is for log of KV pair ops
	p.Prefix = "KV" + self.encode(p.Prefix)
	p.Suffix = colon.Escape(p.Suffix)
	entries := List{}
	var e error
	var from string
	backend := cls[0]
	//Check the availability of the master
	from, e = self.bs.getfrom(hashaddrs[0])
	if e != nil || from == hashaddrs[0] {
		backend = cls[1]
	}
	//Get all KV entries
	if e = backend.ListKeys(p, &entries); e != nil {
		return e
	}
	vch := make(chan string, len(entries.L))
	ech := make(chan error, len(entries.L))
	for _, v := range entries.L {
		//retrive and analysis logs concurrently
		go func(entry string) {
			logs := List{}
			//Get logs of the entry
			if err := backend.ListGet(entry, &logs); err != nil {
				ech <- err
				vch <- ""
				return
			}
			ech <- nil
			logs.L = Deduplicate(logs.L)
			var log LogEvent
			res := ParseString(logs.L[0])
			//Find the latest log
			for _, v := range logs.L {
				log = ParseString(v)
				if log.Clock > res.Clock {
					res = log
				}
			}
			//Get all logs with non-empty value
			if res.KV.Value != "" {
				vch <- self.decode(res.KV.Key)
			} else {
				vch <- ""
			}
		}(v)
	}
	e = nil
	keys := make([]string, 0)
	for i := 0; i < len(entries.L); i++ {
		err := <-ech
		if err != nil {
			e = err
		}
		key := <-vch
		//get all keys
		if key != "" {
			keys = append(keys, key)
		}
	}
	//Let it fail
	if e != nil {
		return e
	}
	//Sort it to make it deterministic
	sort.Strings(keys)
	list.L = keys
	return nil
}

func (self *binclient) ListGet(key string, list *List) error {
	list.L = make([]string, 0)
	cls, hashaddrs := self.bs.getclients(self.hashname)
	//`LS` is for log of list ops
	enckey := self.encode(key)
	logkey := "LS" + enckey
	ls := List{}
	lslog := List{}
	var e error
	var from string
	//Check the availability of the master
	from, e = self.bs.getfrom(hashaddrs[0])
	//Retrive image of checkpoint and log after checkpoint
	//Guarenteed to succeed during recovery mode
	if e != nil || from == hashaddrs[0] {
		if e = cls[1].ListGet(enckey, &ls); e != nil {
			return e
		}
		if e = cls[1].ListGet(logkey, &lslog); e != nil {
			return e
		}
	} else if cls[0].ListGet(enckey, &ls) != nil || cls[0].ListGet(logkey, &lslog) != nil {
		//Backend leave
		if e = cls[1].ListGet(enckey, &ls); e != nil {
			return e
		}
		if e = cls[1].ListGet(logkey, &lslog); e != nil {
			return e
		}
	}
	lslog.L = Deduplicate(lslog.L)
	//Parse logs and sort
	var logs LogSlice
	logs = make([]*LogEvent, len(lslog.L))
	for i, v := range lslog.L {
		t := ParseString(v)
		logs[i] = &t
	}
	sort.Sort(logs)
	result := ls.L
	//Replay the log on the result
	for _, log := range logs {
		result = log.apply(result)
	}
	list.L = result
	return nil
}

func (self *binclient) ListAppend(kv *KeyValue, succ *bool) error {
	cls, hashaddrs := self.bs.getclients(self.hashname)
	var e error
	var from string
	//Check the availability of the master
	if from, e = self.bs.getfrom(hashaddrs[0]); e == nil {
		if from < hashaddrs[0] && self.hashname <= from || from > hashaddrs[0] && self.hashname >= hashaddrs[0] && self.hashname < from {
			//Master joined or just left
			cls = append([]Storage{self.bs.clients[from]}, cls...)
		} else {
			if from, e = self.bs.getfrom(hashaddrs[1]); e == nil {
				//Maybe duplica back or leave
				if hashaddrs[0] < hashaddrs[1] && from > hashaddrs[0] && from < hashaddrs[1] || hashaddrs[0] > hashaddrs[1] && (from > hashaddrs[0] || from < hashaddrs[1]) {
					cls = append([]Storage{self.bs.clients[from]}, cls...)
				} else {
					if from, e = self.bs.getfrom(hashaddrs[2]); e == nil {
						//Maybe duplica back or leave
						if hashaddrs[1] < hashaddrs[2] && from > hashaddrs[1] && from < hashaddrs[2] || hashaddrs[1] > hashaddrs[2] && (from > hashaddrs[1] || from < hashaddrs[2]) {
							cls = append([]Storage{self.bs.clients[from]}, cls...)
						}
					} else {
						//Duplica 2 failure
						cls = append(cls[:2], cls[3])
					}
				}
			} else {
				//Duplica 1 failure
				cls = append(cls[:1], cls[2:]...)
			}
		}
	} else {
		//master failure
		cls = cls[1:]
	}
	/*
		for _, c := range cls {
			fmt.Println(c)
		}
	*/
	kv.Key = self.encode(kv.Key)
	var t uint64
	//Get clock
	if e = self.Clock(uint64(0), &t); e != nil {
		return e
	}
	log := LogEvent{Clock: t, KV: *kv, CMD: "A"}
	logkv := KeyValue{Key: "LS" + kv.Key, Value: log.toString()}
	//Write log to backends
	if e = self.writelog(logkv, cls); e != nil {
		*succ = false
		return e
	}
	*succ = true
	return nil
}

//Write log, retrive list, apply localy to get n.
func (self *binclient) ListRemove(kv *KeyValue, n *int) error {
	cls, hashaddrs := self.bs.getclients(self.hashname)
	var e error
	var from string
	//Check the availability of the master
	if from, e = self.bs.getfrom(hashaddrs[0]); e == nil {
		if from < hashaddrs[0] && self.hashname <= from || from > hashaddrs[0] && self.hashname >= hashaddrs[0] && self.hashname < from {
			//Master joined or just left
			cls = append([]Storage{self.bs.clients[from]}, cls...)
		} else {
			if from, e = self.bs.getfrom(hashaddrs[1]); e == nil {
				//Maybe duplica back or leave
				if hashaddrs[0] < hashaddrs[1] && from > hashaddrs[0] && from < hashaddrs[1] || hashaddrs[0] > hashaddrs[1] && (from > hashaddrs[0] || from < hashaddrs[1]) {
					cls = append([]Storage{self.bs.clients[from]}, cls...)
				} else {
					if from, e = self.bs.getfrom(hashaddrs[2]); e == nil {
						//Maybe duplica back or leave
						if hashaddrs[1] < hashaddrs[2] && from > hashaddrs[1] && from < hashaddrs[2] || hashaddrs[1] > hashaddrs[2] && (from > hashaddrs[1] || from < hashaddrs[2]) {
							cls = append([]Storage{self.bs.clients[from]}, cls...)
						}
					} else {
						//Duplica 2 failure
						cls = append(cls[:2], cls[3])
					}
				}
			} else {
				//Duplica 1 failure
				cls = append(cls[:1], cls[2:]...)
			}
		}
	} else {
		//master failure
		cls = cls[1:]
	}
	//Get the list with ListGet and count deleted number
	ls := List{}
	self.ListGet(kv.Key, &ls)
	*n = 0
	for _, v := range ls.L {
		if v == kv.Value {
			*n++
		}
	}
	kv.Key = self.encode(kv.Key)
	var t uint64
	//Get clock
	if e = self.Clock(uint64(0), &t); e != nil {
		return e
	}
	log := LogEvent{Clock: t, KV: *kv, CMD: "R"}
	logkv := KeyValue{Key: "LS" + kv.Key, Value: log.toString()}
	//Write log to backends
	if e = self.writelog(logkv, cls); e != nil {
		*n = 0
		return e
	}
	return nil
}

func (self *binclient) ListKeys(p *Pattern, list *List) error {
	list.L = make([]string, 0)
	cls, hashaddrs := self.bs.getclients(self.hashname)
	//LS is for log of list ops
	p.Prefix = "LS" + self.encode(p.Prefix)
	p.Suffix = colon.Escape(p.Suffix)
	entries := List{}
	var e error
	var from string
	backend := cls[0]
	//Check the availability of the master
	from, e = self.bs.getfrom(hashaddrs[0])
	if e != nil || from == hashaddrs[0] {
		backend = cls[1]
	}
	//Get all LS entries
	if e = backend.ListKeys(p, &entries); e != nil {
		return e
	}
	vch := make(chan string, len(entries.L))
	ech := make(chan error, len(entries.L))
	for _, v := range entries.L {
		//retrive and replay logs concurrently
		go func(entry string) {
			ls := List{}
			//Get the image of last checkpoint
			if err := backend.ListGet(entry[2:], &ls); err != nil {
				ech <- err
				vch <- ""
				return
			}
			tmp := ls.L
			logs := List{}
			//Get logs of the entry
			if err := backend.ListGet(entry, &logs); err != nil {
				ech <- err
				vch <- ""
				return
			}
			logs.L = Deduplicate(logs.L)
			ech <- nil
			var loglst LogSlice
			loglst = make([]*LogEvent, len(logs.L))
			//Parse the log from json string
			for i, v := range logs.L {
				t := ParseString(v)
				loglst[i] = &t
			}
			sort.Sort(loglst)
			for _, log := range loglst {
				tmp = log.apply(tmp)
			}
			//Get all logs with non-empty value
			if len(tmp) > 0 {
				vch <- self.decode(entry[2:])
			} else {
				vch <- ""
			}
		}(v)
	}
	e = nil
	keys := make([]string, 0)
	for i := 0; i < len(entries.L); i++ {
		err := <-ech
		if err != nil {
			e = err
		}
		key := <-vch
		//get all keys
		if key != "" {
			keys = append(keys, key)
		}
	}
	//Let it fail
	if e != nil {
		return e
	}
	//Sort it to make it deterministic
	sort.Strings(keys)
	list.L = keys
	return nil
}

//Get the clock from the master or previous master
func (self *binclient) Clock(atLeast uint64, ret *uint64) error {
	cls, _ := self.bs.getclients(self.hashname)
	if e := cls[0].Clock(atLeast, ret); e != nil {
		return cls[1].Clock(atLeast, ret)
	}
	return nil
}

var _ Storage = new(binclient)
var _ BinStorage = new(bin)
