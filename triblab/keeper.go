package triblab

import (
	"crypto/sha1"
	//	"fmt"
	"hash"
	//"log"
	"sort"
	"strconv"
	"strings"
	"time"
	"trib"
	"trib/colon"
	"trib/store"
)

type keeper struct {
	Cfg         *trib.KeeperConfig
	hashfunc    hash.Hash
	selfhash    string
	Ready       chan<- bool
	leastLogNum int

	Backs      []string                //IP Address of all backs
	backHash   []string                //Sorted hash value of backs
	backState  map[string]bool         //state of backs, alive or not
	clients    map[string]trib.Storage //connect with each backend storage
	ownClients map[string]trib.Storage //clients own by this keeper

	Kprs     []string                //IP address of all keepers
	kprHash  []string                //sorted hash value of keepers
	kprState map[string]bool         //state of keepers, alive or not
	cliKprs  map[string]trib.Storage //clients connected to keepers

	redo bool //Flag for redoMigration
}

func (self *keeper) init() {
	self.hashfunc = sha1.New()
	self.Ready = self.Cfg.Ready
	self.selfhash = getHash(self.hashfunc, self.Cfg.Addr())
	self.leastLogNum = 5
	self.redo = false

	//Init value of backs
	self.Backs = self.Cfg.Backs
	self.backHash = make([]string, len(self.Backs))
	self.backState = make(map[string]bool, len(self.Backs))
	self.clients = make(map[string]trib.Storage, len(self.Backs))
	self.ownClients = make(map[string]trib.Storage, len(self.Backs))
	for i, v := range self.Backs {
		hashvalue := getHash(self.hashfunc, v)
		self.backHash[i] = hashvalue
		self.backState[hashvalue] = false
		self.clients[hashvalue] = NewClient(v)
	}
	sort.Strings(self.backHash)

	//Init value of keepers
	self.Kprs = self.Cfg.Addrs
	self.kprHash = make([]string, len(self.Cfg.Addrs))
	self.kprState = make(map[string]bool, len(self.Cfg.Addrs))
	self.cliKprs = make(map[string]trib.Storage, len(self.Cfg.Addrs))
	for i, v := range self.Kprs {
		hashvalue := getHash(self.hashfunc, v)
		self.kprHash[i] = hashvalue
		self.kprState[hashvalue] = false
		self.cliKprs[hashvalue] = NewClient(v)
	}
	sort.Strings(self.kprHash)

	//start server on keeper
	bc := new(trib.BackConfig)
	bc.Addr = self.Cfg.Addr()
	bc.Store = store.NewStorage()
	bc.Ready = make(chan bool, 1)
	go ServeBack(bc)

	//mark clock in keepers
	self.writekv(self.cliKprs[self.selfhash], "KPRCLOCK", "0")
	//Check state of keepers, backs
	self.checkKprs(true)
	self.updateOwnClients()
	self.checkBacks(0, true) //do not do migration in init
	/*fmt.Println("in keeper init")
	for _,h := range self.backState {
		fmt.Println(h)
	}
	fmt.Println()*/

	// Set value of FROM and BACK
	for haddr, _ := range self.ownClients {
		if self.backState[haddr] == true {
			prehaddr := self.getHaddr(haddr, -1, "Backs")
			self.writekv(self.clients[haddr], "FROM", prehaddr)
		}
	}
	for haddr, _ := range self.ownClients {
		for h, v := range self.backState {
			if v == true {
				self.writekv(self.clients[haddr], "BACK"+h, "true")
			}
		}
	}

	/*printout addr, hash relation
	for _, addr := range self.Backs {
		hash := getHash(self.hashfunc, addr)
		fmt.Println(addr, sort.SearchStrings(self.backHash, hash))
	}*/
}

// find group of backs owned by this keeper
func (self *keeper) updateOwnClients() {
	// Find start hash, and end hash
	curKprHash := getHash(self.hashfunc, self.Cfg.Addr())
	nextKprHash := self.getHaddr(curKprHash, 1, "Kprs")
	self.ownClients = make(map[string]trib.Storage, len(self.Backs))
	// Go through backends, if it is in range, put it in ownClients
	for _, backhash := range self.backHash {
		if curKprHash < nextKprHash {
			if curKprHash <= backhash && backhash < nextKprHash {
				self.ownClients[backhash] = self.clients[backhash]
			}
		} else if curKprHash >= nextKprHash {
			if curKprHash <= backhash || backhash < nextKprHash {
				self.ownClients[backhash] = self.clients[backhash]
			}
		}
	}
	//fmt.Println(self.Cfg.Addr(), len(self.ownClients))
}

//Get Haddr, given an original position and offset
func (self *keeper) getHaddr(haddr string, offset int, bucket string) string {
	if offset == 0 {
		return haddr
	}
	var bucketHash []string
	var bucketState map[string]bool
	if bucket == "Backs" {
		bucketHash = self.backHash
		bucketState = self.backState
	} else if bucket == "Kprs" {
		bucketHash = self.kprHash
		bucketState = self.kprState
	}

	// Get all alive buckets
	aliveBucket := make([]string, 0)
	for _, h := range bucketHash {
		if bucketState[h] == true {
			aliveBucket = append(aliveBucket, h)
		}
	}
	// find index and return
	cur := sort.SearchStrings(aliveBucket, haddr) % len(aliveBucket)
	index := (cur + offset + len(aliveBucket)) % len(aliveBucket)
	if bucketState[haddr] == false && offset > 0 {
		index = (cur - 1 + offset + len(aliveBucket)) % len(aliveBucket)
	}
	return aliveBucket[index]
}

// write kv pair to a client
func (self *keeper) writekv(cli trib.Storage, k string, v string) {
	var succ bool
	kv := trib.KeyValue{k, v}
	cli.Set(&kv, &succ)
}

//tell every client about backend alive info
func (self *keeper) notifyClients(haddr string, value bool) {
	//Notify BACK information
	if value {
		for hash, state := range self.backState {
			if state == true {
				go self.writekv(self.clients[haddr], "BACK"+hash, "true")
			}
		}
	}

	//Notify FROM infomation
	nexthaddr := self.getHaddr(haddr, 1, "Backs")
	if value {
		//D's FROM set to C
		self.writekv(self.clients[nexthaddr], "FROM", haddr)
		//C's FROM set to C
		self.writekv(self.clients[haddr], "FROM", haddr)
	}

	//Notify BACK infomation
	v := ""
	if value {
		v = strconv.FormatBool(value)
	}
	for hash, _ := range self.clients {
		go self.writekv(self.clients[hash], "BACK"+haddr, v)
	}
}

// do key migration for "name" in range hashStart to hashEnd
// From backend From to To
func (self *keeper) doKeyMigration(hashStart, hashEnd, From, To string) {
	var list trib.List
	cltFrom := self.clients[From]
	cltTo := self.clients[To]

	//Migration for keys
	p := trib.Pattern{}
	e := cltFrom.Keys(&p, &list)
	if e != nil {
		//fmt.Println(e)
	}
	ch := make(chan bool, len(list.L))
	for _, k := range list.L {
		go func(k string) {
			var user string
			//potential bug
			if tmp := strings.Split(k, "::"); len(tmp) == 2 {
				// Judge if a user is in range
				user = colon.Unescape(tmp[0])
				userhash := getHash(self.hashfunc, user)
				inrange := false
				if hashStart < hashEnd {
					if hashStart <= userhash && userhash < hashEnd {
						inrange = true
					}
				} else {
					if hashEnd <= userhash || userhash < hashStart {
						inrange = true
					}
				}
				if !inrange {
					ch <- true
					return
				}
				kv := trib.KeyValue{}
				var value string
				var succ bool
				//data migration
				kv.Key = k
				//kv.Value = ""
				//cltTo.Set(&kv, &succ)
				cltFrom.Get(k, &value)
				kv.Value = value
				cltTo.Set(&kv, &succ)
			}
			ch <- true
		}(k)
	}
	for i := 0; i < len(list.L); i++ {
		<-ch
	}
}

// do key migration for "name" in range hashStart to hashEnd
// From backend From to To
// Prefix is used to search for keys
func (self *keeper) doListMigration(hashStart, hashEnd, From, To, Prefix string) {
	var list trib.List
	cltFrom := self.clients[From]
	cltTo := self.clients[To]

	//Migration for list-keys
	var succNum int
	p := trib.Pattern{Prefix, ""}
	e := cltFrom.ListKeys(&p, &list)
	if e != nil {
		//fmt.Println(e)
	}
	ch := make(chan bool, len(list.L))
	for _, k := range list.L {
		go func(k string) {
			var user string
			if tmp := strings.Split(k, "::"); len(tmp) == 2 {
				// Judge if a user is valid and in range
				user = colon.Unescape(strings.TrimPrefix(tmp[0], Prefix))
				//Also migrate all logs
				user = strings.TrimPrefix(user, "KV")
				user = strings.TrimPrefix(user, "LS")
				userhash := getHash(self.hashfunc, user)
				inrange := false
				if hashStart < hashEnd {
					if hashStart <= userhash && userhash < hashEnd {
						inrange = true
					}
				} else {
					if hashStart <= userhash || userhash < hashEnd {
						inrange = true
					}
				}
				if !inrange {
					ch <- true
					return
				}
				tmplist := trib.List{}
				kv := trib.KeyValue{}
				var succ bool
				//list data migration
				kv.Key = k
				cltFrom.ListGet(k, &tmplist)
				//fist delete all
				for _, l := range tmplist.L {
					kv.Value = l
					cltTo.ListRemove(&kv, &succNum)
				}
				//then migrate
				for _, l := range tmplist.L {
					kv.Value = l
					cltTo.ListAppend(&kv, &succ)
				}
			}
			ch <- true
		}(k)
	}
	for i := 0; i < len(list.L); i++ {
		<-ch
	}
}

func (self *keeper) doMigration(hashStart, hashEnd, From, To string) {
	//fmt.Println("do migration from", sort.SearchStrings(self.backHash, From), "to", sort.SearchStrings(self.backHash, To))
	// write recovery info
	info := colon.Escape(hashStart) + "::" + colon.Escape(hashEnd) + "::" + colon.Escape(From) + "::" + colon.Escape(To)
	kv := trib.KeyValue{"REV", info}
	var succ bool
	clt := self.clients[hashEnd]
	clt.Set(&kv, &succ)
	ch := make(chan bool, 2)
	// do key and list migration
	go func() {
		self.doKeyMigration(hashStart, hashEnd, From, To)
		ch <- true
	}()
	go func() {
		self.doListMigration(hashStart, hashEnd, From, To, "")
		ch <- true
	}()
	<-ch
	<-ch

	// erase recovery info
	kv.Value = ""
	clt.Set(&kv, &succ)
}

func (self *keeper) redoMigration() {
	//Check if redo not done yet.
	if self.redo {
		return
	}
	self.redo = true
	var value string
	for h, cli := range self.ownClients {
		//Check all of its clients if it need remigration
		if self.backState[h] == false {
			continue
		}
		cli.Get("REV", &value)
		if value == "" {
			continue
		}
		//Get parameters and remigrate
		infos := strings.Split(value, "")
		if len(infos) != 4 {
			continue
		}
		hashStart := colon.Unescape(infos[0])
		hashEnd := colon.Unescape(infos[1])
		From := colon.Unescape(infos[2])
		To := colon.Unescape(infos[3])
		self.doMigration(hashStart, hashEnd, From, To)
	}
	self.redo = false
}

//do migration when addr is value(t/f)
// value = true: back is back
// value = false: back is down
func (self *keeper) handleMigration(haddr string, value bool) {
	/*fmt.Println("keeper", self.Cfg.Addr(), "in handleMigration")

	for i, _ := range self.backHash {
		fmt.Println(self.backState[self.backHash[i]])
	}
	/*
		for i,_ := range(self.backHash) {
			fmt.Println(self.backState[self.backHash[i]])
		}
	*/
	pre3hash := self.getHaddr(haddr, -3, "Backs")
	pre2hash := self.getHaddr(haddr, -2, "Backs")
	pre1hash := self.getHaddr(haddr, -1, "Backs")
	next1hash := self.getHaddr(haddr, 1, "Backs")
	next2hash := self.getHaddr(haddr, 2, "Backs")
	next3hash := self.getHaddr(haddr, 3, "Backs")
	//fmt.Println(sort.SearchStrings(self.backHash, pre3hash), sort.SearchStrings(self.backHash, pre2hash), sort.SearchStrings(self.backHash, pre1hash), sort.SearchStrings(self.backHash, haddr), sort.SearchStrings(self.backHash, next1hash), sort.SearchStrings(self.backHash, next2hash), sort.SearchStrings(self.backHash, next3hash))

	//do migration
	if value {
		self.doMigration(pre3hash, pre2hash, next1hash, haddr)
		self.doMigration(pre2hash, pre1hash, next2hash, haddr)
		self.doMigration(pre1hash, haddr, next3hash, haddr)
		self.writekv(self.clients[haddr], "FROM", pre1hash)
	} else {
		self.doMigration(pre3hash, pre2hash, pre1hash, next1hash)
		self.doMigration(pre2hash, pre1hash, next1hash, next2hash)
		//E->F
		self.doMigration(pre1hash, haddr, next2hash, next3hash)
		self.writekv(self.clients[next1hash], "FROM", pre1hash)
	}
}

// Check if Keepers are alive
// If keeper state change, updateOwnClients
func (self *keeper) checkKprs(init bool) uint64 {
	var max uint64
	var str string
	flagKpr := true
	max = 0
	ch := make(chan uint64, len(self.cliKprs))
	for haddr, cli := range self.cliKprs {
		go func(haddr string, cli trib.Storage) {
			//Send clock to every keeper
			e := cli.Get("KPRCLOCK", &str)
			ret, _ := strconv.Atoi(str)
			//e := cli.Clock(max, &ret)
			if e == nil {
				if self.kprState[haddr] == false {
					self.kprState[haddr] = true
					flagKpr = false
				}
			} else {
				if self.kprState[haddr] == true {
					self.kprState[haddr] = false
					flagKpr = false
				}
			}
			ch <- uint64(ret)
		}(haddr, cli)
	}
	//wait for all go routines to finish
	for i := 0; i < len(self.cliKprs); i++ {
		ret := <-ch
		if ret > max {
			max = ret
		}
	}
	// If keeper state changed, update keeper's ownClients
	if !flagKpr {
		/*for _,v := range self.kprState {
			fmt.Println(v)
		}*/
		self.updateOwnClients()
		if !init {
			go self.redoMigration()
		}
	}
	self.writekv(self.cliKprs[self.selfhash], "KPRCLOCK", strconv.FormatUint(max, 10))
	return max
}

// Check if backs are alive
// If init is true, do not do data migration
// Else, should do data migration and chagne "BACK" info
func (self *keeper) checkBacks(clk uint64, init bool) uint64 {
	var tmp uint64
	var e error
	tmp = clk
	ch := make(chan uint64, len(self.clients))
	for haddr, cli := range self.clients {
		go func(haddr string, cli trib.Storage) {
			if _, found := self.ownClients[haddr]; found == true {
				var ret uint64
				e = cli.Clock(clk, &ret)
				ch <- ret
			} else {
				var str string
				e = cli.Get("FROM", &str)
				ch <- uint64(0)
			}

			if e == nil {
				if self.backState[haddr] == false {
					self.backState[haddr] = true
					//fmt.Println(self.Cfg.Addr(), "notice a back return")
					//if belone to keeper, notify client and migration
					if _, found := self.ownClients[haddr]; found == false {
						return
					}
					self.notifyClients(haddr, true)
					if !init {
						go self.handleMigration(haddr, true)
					}
				}
			} else {
				if self.backState[haddr] == true {
					self.backState[haddr] = false
					//no error handling here, should be ok
					//fmt.Println(self.Cfg.Addr(), "notice a back leave")
					//if belone to keeper, notify client and migration
					if _, found := self.ownClients[haddr]; found == false {
						return
					}

					self.notifyClients(haddr, false)
					if !init {
						go self.handleMigration(haddr, false)
					}
				}
			}
		}(haddr, cli)
	}
	for i := 0; i < len(self.clients); i++ {
		ret := <-ch
		if ret > tmp {
			tmp = ret
		}
	}
	return tmp
}

// periotically check Keepers and Backs
func (self *keeper) heartBeat() {
	var max uint64
	max = 0
	tick := time.Tick(time.Second)
	for {
		select {
		case <-tick:
			go func() {
				//check keepers
				max = self.checkKprs(false)
				//check backends
				max = self.checkBacks(max, false)
			}()
		//Printout current back state
		/*for i,_ := range(self.backHash) {
			fmt.Println(self.backState[self.backHash[i]])
		}*/
		default:
			time.Sleep(100 * time.Millisecond)
		}
	}
}

func (self *keeper) simplifyLog(haddr, prefix string) {
	var list, tmplist trib.List
	var kv trib.KeyValue

	clt := self.clients[haddr]
	p := trib.Pattern{Prefix: prefix, Suffix: ""}
	e := clt.ListKeys(&p, &list)
	if e != nil {
		//fmt.Println(e)
	}
	//client in master range
	//read
	//sort, duplicate elimation, replay
	/*replay(cli)
	 */
	//List-Keys, find all list-keys on server
	for _, k := range list.L {
		var user string
		if tmp := strings.Split(k, "::"); len(tmp) == 2 {
			// check if a user is valid
			user = colon.Unescape(strings.TrimPrefix(tmp[0], prefix))
			/*if !trib.IsValidUsername(user) {
				continue
			}*/
			//check if username is in master range
			userhash := getHash(self.hashfunc, user)
			hashEnd := haddr
			hashStart := self.getHaddr(haddr, -1, "Backs")
			inrange := false
			if hashStart < hashEnd {
				if hashStart <= userhash && userhash < hashEnd {
					inrange = true
				}
			} else {
				if hashStart <= userhash || userhash < hashEnd {
					inrange = true
				}
			}
			if !inrange {
				continue
			}
			// List-Get, get a specific list
			kv.Key = k
			clt.ListGet(k, &tmplist)
			//deduplicate
			tmplist.L = Deduplicate(tmplist.L)
			//for each log in tmplist.L, change to logevent, sort
			var logs LogSlice
			logs = make([]*LogEvent, len(tmplist.L))
			for i, v := range tmplist.L {
				l := ParseString(v)
				logs[i] = &l
			}
			sort.Sort(logs)
			// find dup1 and dup2
			dup1hash := self.getHaddr(haddr, 1, "Backs")
			dup2hash := self.getHaddr(haddr, 2, "Backs")
			cltdup1 := self.clients[dup1hash]
			cltdup2 := self.clients[dup2hash]
			// replay
			// if error occurs, this means that backs go down
			// continue this round and break
			hasErr := false
			var err0, err1, err2 error
			for i := 0; i < len(logs)-self.leastLogNum; i++ {
				err0 = logs[i].replay(clt)
				if dup1hash != haddr {
					err1 = logs[i].replay(cltdup1)
				}
				if dup2hash != dup1hash && dup2hash != haddr {
					err2 = logs[i].replay(cltdup2)
				}
				if err0 != nil || err1 != nil || err2 != nil {
					hasErr = true
					break
				}
			}
			if hasErr {
				break
			}
		}
	} //end of reading all keys on backend

}

//Periotically manage logs
func (self *keeper) manageLog() {
	for {
		for haddr, _ := range self.ownClients {
			if self.backState[haddr] == false {
				continue
			}
			// check if back in recovery mode
			clt := self.clients[haddr]
			var state string
			clt.Get("REV", &state)
			if state != "" {
				continue
			}

			// simplify log
			self.simplifyLog(haddr, "KV")
			self.simplifyLog(haddr, "LS")
		}
		time.Sleep(time.Second)
	}
}

//TODO:keeper doing migration, then it down
func (self *keeper) Run() error {
	self.init()

	if self.Ready != nil {
		self.Ready <- true
	}

	go self.heartBeat()
	go self.manageLog()

	//Don't know what to return...
	return nil
}
