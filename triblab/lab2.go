package triblab

import (
	//	"fmt"
	"trib"
)

func NewBinClient(backs []string) trib.BinStorage {
	bs := &bin{backs: backs}
	bs.Init()
	return bs
}

func ServeKeeper(kc *trib.KeeperConfig) error {
	kp := &keeper{Cfg: kc}
	return kp.Run()
}

func NewFront(s trib.BinStorage) trib.Server {
	front := &frontserver{bs: s}
	front.Init()
	return front
}
