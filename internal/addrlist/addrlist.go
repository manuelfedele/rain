package addrlist

import (
	"net"
	"sort"
	"time"

	"github.com/cenkalti/rain/internal/blocklist"
	"github.com/cenkalti/rain/internal/externalip"
	"github.com/cenkalti/rain/internal/peerpriority"
	"github.com/google/btree"
)

type PeerSource int

const (
	Tracker PeerSource = iota
	DHT
	PEX
	Manual
)

// AddrList contains peer addresses that are ready to be connected.
type AddrList struct {
	peerByTime     []*peerAddr
	peerByPriority *btree.BTree

	maxItems   int
	listenPort int
	clientIP   *net.IP
	blocklist  *blocklist.Blocklist

	countBySource map[PeerSource]int
}

func New(maxItems int, blocklist *blocklist.Blocklist, listenPort int, clientIP *net.IP) *AddrList {
	return &AddrList{
		peerByPriority: btree.New(2),

		maxItems:      maxItems,
		listenPort:    listenPort,
		clientIP:      clientIP,
		blocklist:     blocklist,
		countBySource: make(map[PeerSource]int),
	}
}

func (d *AddrList) Reset() {
	d.peerByTime = nil
	d.peerByPriority.Clear(false)
	d.countBySource = make(map[PeerSource]int)
}

func (d *AddrList) Len() int {
	return d.peerByPriority.Len()
}

func (d *AddrList) LenSource(s PeerSource) int {
	return d.countBySource[s]
}

func (d *AddrList) Pop() *net.TCPAddr {
	item := d.peerByPriority.DeleteMax()
	if item == nil {
		return nil
	}
	p := item.(*peerAddr)
	d.peerByTime[p.index] = nil
	d.countBySource[p.source]--
	return p.addr
}

func (d *AddrList) Push(addrs []*net.TCPAddr, source PeerSource) {
	now := time.Now()
	var added int
	for _, ad := range addrs {
		// 0 port is invalid
		if ad.Port == 0 {
			continue
		}
		// Discard own client
		if ad.IP.IsLoopback() && ad.Port == d.listenPort {
			continue
		} else if d.clientIP.Equal(ad.IP) {
			continue
		}
		if externalip.IsExternal(ad.IP) {
			continue
		}
		if d.blocklist != nil && d.blocklist.Blocked(ad.IP) {
			continue
		}
		p := &peerAddr{
			addr:      ad,
			timestamp: now,
			source:    source,
			priority:  peerpriority.Calculate(ad, d.clientAddr()),
		}
		item := d.peerByPriority.ReplaceOrInsert(p)
		if item != nil {
			d.peerByTime[item.(*peerAddr).index] = p
		} else {
			d.peerByTime = append(d.peerByTime, p)
			added++
		}
	}
	d.filterNils()
	sort.Sort(byTimestamp(d.peerByTime))
	d.countBySource[source] += added

	delta := d.peerByPriority.Len() - d.maxItems
	if delta > 0 {
		d.removeExcessItems(delta)
		d.filterNils()
		d.countBySource[source] -= delta
	}
	if len(d.peerByTime) != d.peerByPriority.Len() {
		panic("addr list data structures not in sync")
	}
}

func (d *AddrList) filterNils() {
	b := d.peerByTime[:0]
	for _, x := range d.peerByTime {
		if x != nil {
			b = append(b, x)
			x.index = len(b) - 1
		}
	}
	d.peerByTime = b
}

func (d *AddrList) removeExcessItems(delta int) {
	for i := 0; i < delta; i++ {
		d.peerByPriority.Delete(d.peerByTime[i])
		d.peerByTime[i] = nil
	}
}

func (d *AddrList) clientAddr() *net.TCPAddr {
	ip := *d.clientIP
	if ip == nil {
		ip = net.IPv4(0, 0, 0, 0)
	}
	return &net.TCPAddr{
		IP:   ip,
		Port: d.listenPort,
	}
}
