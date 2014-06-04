package triblab

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"sync"
	"time"
	. "trib"
)

//Implement Tribble Order
type TribSlice []*Trib

func (ts TribSlice) Len() int {
	return len(ts)
}

func (ts TribSlice) Less(i, j int) bool {
	ti := ts[i]
	tj := ts[j]
	if ti.Clock != tj.Clock {
		return ti.Clock < tj.Clock
	}
	if !ti.Time.Equal(tj.Time) {
		return ti.Time.Before(tj.Time)
	}
	if ti.User != tj.User {
		return ti.User < tj.User
	}
	return ti.Message < tj.Message
}

func (ts TribSlice) Swap(i, j int) {
	ts[i], ts[j] = ts[j], ts[i]
}

//Marshal Trib to json in order to store into the Storage.
func Marshal(tribble Trib) string {
	b, _ := json.Marshal(tribble)
	return string(b)
}

func Unmarshal(t string) *Trib {
	var tribble Trib
	json.Unmarshal([]byte(t), &tribble)
	return &tribble
}

//Data structure for the cache
type userentry struct {
	version  uint64    //the version of this cache
	triblist TribSlice //the data, possibly be nil list
}

type followentry struct {
	version    uint64
	followlist []string
}

/*
 * I chose hashmaps to cache the list data, since these communications are expensive,
 * Every time reading a cache, I have to get the timestamp stored in the backend storage.
 * By doing that, I can get avoid from many expensive list-gets.
 * I update the cache in a lazy manner, that is, update the cache only when I have to
 * retrive its data from the backends.
 * The size of the cache as a hashmap will increase with the time. A LRU manner cache would
 * use the memory efficiently. However I don't have the energy to implement that this time.
 */
type frontserver struct {
	bs          BinStorage //the bin storage
	userinfo    Storage    //the bin saves user's information
	totallock   sync.Mutex
	locks       map[string]*sync.Mutex  //Lock table, one lock per user, increasing parallism.
	dataCache   map[string]*userentry   //cache user's tribbles
	followCache map[string]*followentry //cache user's following list
	clients     map[string]Storage      //reuse storage clients created previously
	listusers   []string
}

//get a lock
func (self *frontserver) lock(name string) *sync.Mutex {
	//Only allow create lock one by one.
	self.totallock.Lock()
	defer self.totallock.Unlock()
	if lk, found := self.locks[name]; found {
		return lk
	}
	lk := new(sync.Mutex)
	self.locks[name] = lk
	return lk
}

//reuses storage created previously
func (self *frontserver) Bin(name string) Storage {
	if cl, found := self.clients[name]; found {
		return cl
	}
	cl := self.bs.Bin(name)
	self.clients[name] = cl
	return cl
}

func (self *frontserver) Init() {
	self.userinfo = self.bs.Bin("USERS")
	self.locks = make(map[string]*sync.Mutex)
	self.dataCache = make(map[string]*userentry)
	self.followCache = make(map[string]*followentry)
	self.clients = make(map[string]Storage)
	self.listusers, _ = self.ListUsers()
}

//Helper function detects whether a user exists and returns the version on this frontend.
func (self *frontserver) findUser(user string) (uint64, error) {
	//Check whether it is cached
	meta, found := self.dataCache[user]
	if found {
		return meta.version, nil
	}
	//Not in cache, retrive from back storage.
	ver, err := self.getVersion(user)
	if err != nil {
		return uint64(0), err
	}
	//Only holds the cache entry, has to retrive data when needed.
	entry := userentry{version: uint64(0)}
	self.dataCache[user] = &entry
	return ver, nil
}

//Helper function retrives the version number of the user.
func (self *frontserver) getVersion(user string) (uint64, error) {
	var t string
	err := self.userinfo.Get(user, &t)
	if err != nil {
		return uint64(0), err
	}
	if t == "" {
		return uint64(0), fmt.Errorf("user %q not exists", user)
	}
	i, _ := strconv.Atoi(t)
	return uint64(i), nil
}

//Get the version of the user's following list
func (self *frontserver) getFollowingVersion(user string) (uint64, error) {
	var t string
	err := self.Bin(user+"FOLLOWING").Get("FOLLOWVERSION", &t)
	if err != nil {
		return uint64(0), err
	}
	if t == "" {
		return uint64(0), fmt.Errorf("user %q not exists", user)
	}
	i, _ := strconv.Atoi(t)
	return uint64(i), nil
}

//Get the current clock
func (self *frontserver) getClock(srv Storage) (uint64, error) {
	var t uint64
	e := srv.Clock(uint64(0), &t)
	if e != nil {
		return uint64(0), e
	}
	return t, nil
}

//Updates userinfo timestamp
func (self *frontserver) updateVersion(user string, t uint64) error {
	var b bool
	kv := KeyValue{Key: user, Value: strconv.FormatUint(t, 10)}
	return self.userinfo.Set(&kv, &b)
}

//updates user following list timestamp
func (self *frontserver) updateFollowVersion(user string, t uint64) error {
	var b bool
	kv := KeyValue{Key: "FOLLOWVERSION", Value: strconv.FormatUint(t, 10)}
	return self.Bin(user+"FOLLOWING").Set(&kv, &b)
}

func (self *frontserver) SignUp(user string) error {
	//Check username
	if len(user) > MaxUsernameLen {
		return fmt.Errorf("username %q too long", user)
	}

	if !IsValidUsername(user) {
		return fmt.Errorf("invalid username %q", user)
	}
	//needs to lock since issues write
	self.lock(user).Lock()
	defer self.lock(user).Unlock()
	//Check whether username exists
	_, e := self.findUser(user)
	if e == nil {
		return fmt.Errorf("user %q already exists", user)
	}
	//Register user with new timestamp
	var t uint64
	t, e = self.getClock(self.userinfo)
	if e != nil {
		return e
	}
	//Has to issue two writes, the following version number and user info.
	if e = self.updateFollowVersion(user, t); e != nil {
		return e
	}
	if e = self.updateVersion(user, t); e != nil {
		return e
	}
	//cache user
	entry := userentry{version: t}
	self.dataCache[user] = &entry
	return nil
}

func (self *frontserver) ListUsers() ([]string, error) {
	//Cache hits
	if len(self.listusers) >= MinListUser {
		return self.listusers, nil
	}
	//get all usernames from userinfo table
	l := &List{}
	p := &Pattern{Prefix: "", Suffix: ""}
	if err := self.userinfo.Keys(p, l); err != nil {
		return nil, err
	}
	ret := l.L
	//returned usernames should be sorted in alphabetical order
	sort.Strings(ret)
	for _, user := range ret {
		//username not in local cache, create a new entry
		_, found := self.dataCache[user]
		if !found {
			entry := userentry{version: uint64(0)}
			self.dataCache[user] = &entry
		}
	}
	//truncates the list when length over MinListUser
	if len(ret) > MinListUser {
		ret = ret[:MinListUser]
	}
	//Cache the result
	self.listusers = ret

	return ret, nil
}

func (self *frontserver) IsFollowing(who, whom string) (bool, error) {
	if who == whom {
		return false, fmt.Errorf("checking the same user")
	}
	var e error
	//Checks whether both user exists
	if _, e = self.findUser(whom); e != nil {
		return false, e
	}
	if _, e = self.findUser(who); e != nil {
		return false, e
	}
	l := &List{}
	if e = self.Bin(who+"FOLLOWING").ListGet(whom, l); e != nil {
		return false, e
	}
	return len(l.L) > 0, nil
}

func (self *frontserver) Follow(who, whom string) error {
	if who == whom {
		return fmt.Errorf("cannot follow oneself")
	}
	var e error
	//Check user existance
	if _, e = self.findUser(whom); e != nil {
		return e
	}
	//Lock since writes are issued
	self.lock(who + "FOLLOWING").Lock()
	defer self.lock(who + "FOLLOWING").Unlock()
	//Check if trying to follow too much users
	var follows []string
	if follows, e = self.Following(who); e != nil {
		return e
	}
	if len(follows) >= MaxFollowing {
		return fmt.Errorf("cannot follow more users, limit of %d reached", MaxFollowing)
	}
	//Check if already following
	isfollow := false
	for _, user := range follows {
		if user == whom {
			isfollow = true
			break
		}
	}
	if isfollow {
		return fmt.Errorf("user %q is already following %q", who, whom)
	}
	//get the new clock
	var t uint64
	if t, e = self.getClock(self.Bin(who)); e != nil {
		return e
	}
	//update follow version
	if e = self.updateFollowVersion(who, t); e != nil {
		return e
	}
	//list append
	kv := KeyValue{Key: whom, Value: "followed"}
	var b bool
	return self.Bin(who+"FOLLOWING").ListAppend(&kv, &b)
}

//no need to retrive the follow list
func (self *frontserver) Unfollow(who, whom string) error {
	if who == whom {
		return fmt.Errorf("cannot unfollow oneself")
	}
	//Checks whether both users exist
	var e error
	if _, e = self.findUser(whom); e != nil {
		return e
	}
	if _, e = self.findUser(who); e != nil {
		return e
	}
	//Lock since writes are issued
	self.lock(who + "FOLLOWING").Lock()
	defer self.lock(who + "FOLLOWING").Unlock()
	//gets the new clock
	var t uint64
	if t, e = self.getClock(self.Bin(who)); e != nil {
		return e
	}
	//update follow version
	if e = self.updateFollowVersion(who, t); e != nil {
		return e
	}
	//list remove
	kv := KeyValue{Key: whom, Value: "followed"}
	var i int
	if e = self.Bin(who+"FOLLOWING").ListRemove(&kv, &i); e != nil {
		return e
	}
	//if no value is removed, `who` is not following `whom`
	if i == 0 {
		return fmt.Errorf("user %q is not following %q", who, whom)
	}
	return nil
}

func (self *frontserver) Following(who string) ([]string, error) {
	//Check whether the user exists
	ver, e := self.findUser(who)
	if e != nil {
		return nil, e
	}
	//Gets FOLLOWVERSION
	ver, e = self.getFollowingVersion(who)
	if e != nil {
		return nil, e
	}
	//Checks if current version is outdated
	if entry, found := self.followCache[who]; found {
		if entry.version >= ver {
			return entry.followlist, nil
		}
	}
	pat := Pattern{Prefix: "", Suffix: ""}
	list := List{}
	e = self.Bin(who+"FOLLOWING").ListKeys(&pat, &list)
	if e != nil {
		return nil, e
	}
	result := list.L
	sort.Strings(result)
	//Cache the result
	entry := followentry{version: ver, followlist: result}
	self.followCache[who] = &entry
	return result, nil
}

func (self *frontserver) Post(user, post string, c uint64) error {
	if len(post) > MaxTribLen {
		return fmt.Errorf("trib too long")
	}
	var t uint64
	var e error
	//Check whether user exists
	if _, e = self.findUser(user); e != nil {
		return e
	}
	//Lock since writes are issued
	self.lock(user).Lock()
	defer self.lock(user).Unlock()
	//Get the recent clock
	if t, e = self.getClock(self.Bin(user)); e != nil {
		return e
	}
	tribble := Trib{User: user, Message: post, Time: time.Now(), Clock: t}
	//Save the trib into the user's trib list
	if e = self.updateVersion(user, t); e != nil {
		return e
	}
	kv := KeyValue{Key: "TRIBLIST", Value: Marshal(tribble)}
	b := false
	return self.Bin(user).ListAppend(&kv, &b)
}

func (self *frontserver) Home(user string) ([]*Trib, error) {
	var e error
	var follows []string
	//Retrive the user's follow list, has to return the user's own tribs
	if follows, e = self.Following(user); e != nil {
		return make([]*Trib, 0), e
	}
	follows = append(follows, user)
	ch := make(chan []*Trib, len(follows))
	defer close(ch)
	//Concurrently retrive tribs of all followees
	for _, name := range follows {
		go func(who string) {
			tr, er := self.Tribs(who)
			ch <- tr
			if er != nil {
				e = er
			}
		}(name)
	}
	nfollow := len(follows)
	var result TribSlice
	result = make([]*Trib, 0)
	for i := 0; i < nfollow; i++ {
		//We should return error if one of the query is failed.
		if e != nil {
			return make([]*Trib, 0), e
		}
		result = append(result, <-ch...)
	}
	//Sort the subscriptions in tribble order
	sort.Sort(result)
	begin := 0
	if len(result) > MaxTribFetch {
		begin = len(result) - 100
	}
	return result[begin:], nil
}

func (self *frontserver) Tribs(user string) ([]*Trib, error) {
	var t uint64
	var e error
	//CHeck user existance
	if _, e = self.findUser(user); e != nil {
		return make([]*Trib, 0), e
	}
	//get the version of user's data
	if t, e = self.getVersion(user); e != nil {
		return make([]*Trib, 0), e
	}
	//Check cache
	if entry, found := self.dataCache[user]; found && entry.version >= t {
		if len(entry.triblist) > MaxTribFetch {
			entry.triblist = entry.triblist[:100]
		}
		return entry.triblist, nil
	}
	//Retrive the list from back storage
	l := List{}
	if e = self.Bin(user).ListGet("TRIBLIST", &l); e != nil {
		return make([]*Trib, 0), e
	}
	begin := 0
	if len(l.L) > MaxTribFetch {
		begin = len(l.L) - MaxTribFetch
	}
	//How about do some garbage collection here?
	rmvlst := l.L[:begin]
	if len(rmvlst) > 0 {
		self.lock(user).Lock()
		defer self.lock(user).Unlock()
		for _, v := range rmvlst {
			go func(tribble string) {
				kv := &KeyValue{Key: "TRIBLIST", Value: tribble}
				var n int
				self.Bin(user).ListRemove(kv, &n)
			}(v)
		}
	}
	list := l.L[begin:]
	ll := len(list) - 1
	var result TribSlice
	result = make([]*Trib, len(list))
	for i, v := range list {
		//fmt.Println(v)
		result[ll-i] = Unmarshal(v)
	}
	//Don't need to sort since it uses list append.
	//Sort the tribs into tribble order
	//TMD we still need to sort it due to concurrent writes...orz
	sort.Sort(result)
	//Cache the result of course
	entry := userentry{version: t, triblist: result}
	self.dataCache[user] = &entry
	return result, nil
}

var _ Server = new(frontserver)
