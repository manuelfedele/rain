package rain

import (
	"crypto/rand"
	"net"
	"sync"

	"github.com/cenkalti/log"

	"github.com/cenkalti/rain/internal/logger"
	"github.com/cenkalti/rain/internal/protocol"
	"github.com/cenkalti/rain/internal/torrent"
)

// Limits
const (
	maxPeerServe      = 200
	maxPeerPerTorrent = 50
	downloadSlots     = 4
	uploadSlots       = 4
)

// http://www.bittorrent.org/beps/bep_0020.html
var peerIDPrefix = []byte("-RN" + build + "-")
var build = "0001"

func SetLogLevel(l log.Level) { logger.LogLevel = l }

type Rain struct {
	config        *Config
	peerID        protocol.PeerID
	listener      *net.TCPListener
	transfers     map[protocol.InfoHash]*transfer // all active transfers
	transfersSKey map[[20]byte]*transfer          // for encryption
	transfersM    sync.Mutex
	log           logger.Logger
}

// New returns a pointer to new Rain BitTorrent client.
func New(c *Config) (*Rain, error) {
	peerID, err := generatePeerID()
	if err != nil {
		return nil, err
	}
	return &Rain{
		config:        c,
		peerID:        peerID,
		transfers:     make(map[protocol.InfoHash]*transfer),
		transfersSKey: make(map[[20]byte]*transfer),
		log:           logger.New("rain"),
	}, nil
}

func generatePeerID() (protocol.PeerID, error) {
	var id protocol.PeerID
	copy(id[:], peerIDPrefix)
	_, err := rand.Read(id[len(peerIDPrefix):])
	return id, err
}

// Listen peer port and accept incoming peer connections.
func (r *Rain) Listen() error {
	var err error
	addr := &net.TCPAddr{Port: r.config.Port}
	r.listener, err = net.ListenTCP("tcp4", addr)
	if err != nil {
		return err
	}
	r.log.Notice("Listening peers on tcp://" + r.listener.Addr().String())
	go r.accepter()
	return nil
}

func (r *Rain) Close() error { return r.listener.Close() }

func (r *Rain) accepter() {
	limit := make(chan struct{}, maxPeerServe)
	for {
		conn, err := r.listener.Accept()
		if err != nil {
			r.log.Error(err)
			return
		}
		limit <- struct{}{}
		go func(conn net.Conn) {
			defer func() {
				// if err := recover(); err != nil {
				// r.log.Critical(err)
				// }
				conn.Close()
				<-limit
			}()
			r.servePeerConn(newPeer(conn))
		}(conn)
	}
}

func (r *Rain) PeerID() protocol.PeerID { return r.peerID }
func (r *Rain) Port() uint16            { return uint16(r.listener.Addr().(*net.TCPAddr).Port) }

func (r *Rain) servePeerConn(p *peer) {
	p.log.Debugln("Serving peer", p.conn.RemoteAddr())

	_, _, ih, _, err := handshakeIncoming(p.conn, r.config.Encryption.ForceIncoming,
		func(sKeyHash [20]byte) (sKey []byte) {
			r.transfersM.Lock()
			t, ok := r.transfersSKey[sKeyHash]
			r.transfersM.Unlock()
			if ok {
				sKey = t.torrent.Info.Hash[:]
			}
			return
		},
		func(ih protocol.InfoHash) bool {
			r.transfersM.Lock()
			_, ok := r.transfers[ih]
			r.transfersM.Unlock()
			return ok
		},
		[8]byte{}, r.peerID)
	if err != nil {
		if err == errOwnConnection {
			r.log.Debug(err)
		} else {
			r.log.Error(err)
		}
		return
	}

	r.transfersM.Lock()
	t, ok := r.transfers[ih]
	r.transfersM.Unlock()
	if !ok {
		r.log.Debug("Transfer is removed during incoming handshake")
		return
	}

	p.log.Debugln("servePeerConn: Handshake completed")
	p.Serve(t)
}

// Download starts a download and waits for it to finish.
func (r *Rain) Download(torrentPath, where string) error {
	torrent, err := torrent.New(torrentPath)
	if err != nil {
		return err
	}
	r.log.Debugf("Parsed torrent file: %#v", torrent)

	t, err := r.newTransfer(torrent, where)
	if err != nil {
		return err
	}

	go t.Run()
	<-t.Finished

	return nil
}

func (r *Rain) DownloadMagnet(url, where string) error {
	panic("not implemented")
}
