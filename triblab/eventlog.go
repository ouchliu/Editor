package triblab

import (
	"encoding/json"
	. "trib"
)

//The events saved into log
type LogEvent struct {
	Clock uint64
	KV    KeyValue
	CMD   string
}

func (self LogEvent) toString() string {
	b, _ := json.Marshal(self)
	return string(b)
}

func (self LogEvent) fromString(l string) {
	json.Unmarshal([]byte(l), &self)
}

func (self LogEvent) apply(data []string) []string {
	switch self.CMD {
	case "A":
		return append(data, self.KV.Value)
	case "R":
		tmp := make([]string, 0)
		for _, v := range data {
			if v != self.KV.Value {
				tmp = append(tmp, v)
			}
		}
		return tmp
	}
	return nil
}

//apply this log to a given storage, and remove the log from the backend
func (self LogEvent) replay(store Storage) error {
	var b bool
	var n int
	var e error
	var kv KeyValue
	switch self.CMD {
	case "S":
		if e = store.Set(&self.KV, &b); e != nil {
			return e
		}
		kv = KeyValue{Key: "KV" + self.KV.Key, Value: self.toString()}
		return store.ListRemove(&kv, &n)
	case "A":
		if e = store.ListAppend(&self.KV, &b); e != nil {
			return e
		}
		kv = KeyValue{Key: "LS" + self.KV.Key, Value: self.toString()}
		return store.ListRemove(&kv, &n)
	case "R":
		if e = store.ListRemove(&self.KV, &n); e != nil {
			return e
		}
		kv = KeyValue{Key: "LS" + self.KV.Key, Value: self.toString()}
		return store.ListRemove(&kv, &n)
	}
	return nil
}

func ParseString(l string) LogEvent {
	log := LogEvent{}
	json.Unmarshal([]byte(l), &log)
	return log
}

type LogSlice []*LogEvent

func (ls LogSlice) Less(i, j int) bool {
	return ls[i].Clock < ls[j].Clock
}

func (ls LogSlice) Len() int {
	return len(ls)
}

func (ls LogSlice) Swap(i, j int) {
	ls[i], ls[j] = ls[j], ls[i]
}
