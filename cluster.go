/**
 * OpenBmclAPI (Golang Edition)
 * Copyright (C) 2023 Kevin Z <zyxkad@gmail.com>
 * All rights reserved
 *
 *  This program is free software: you can redistribute it and/or modify
 *  it under the terms of the GNU Affero General Public License as published
 *  by the Free Software Foundation, either version 3 of the License, or
 *  (at your option) any later version.
 *
 *  This program is distributed in the hope that it will be useful,
 *  but WITHOUT ANY WARRANTY; without even the implied warranty of
 *  MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 *  GNU Affero General Public License for more details.
 *
 *  You should have received a copy of the GNU Affero General Public License
 *  along with this program.  If not, see <https://www.gnu.org/licenses/>.
 */

package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/LiterMC/socket.io"
	"github.com/LiterMC/socket.io/engine.io"
	"github.com/gorilla/websocket"
	"github.com/gregjones/httpcache"

	gocache "github.com/LiterMC/go-openbmclapi/cache"
	"github.com/LiterMC/go-openbmclapi/database"
	"github.com/LiterMC/go-openbmclapi/internal/build"
	"github.com/LiterMC/go-openbmclapi/limited"
	"github.com/LiterMC/go-openbmclapi/log"
	"github.com/LiterMC/go-openbmclapi/notify"
	"github.com/LiterMC/go-openbmclapi/notify/email"
	"github.com/LiterMC/go-openbmclapi/notify/webpush"
	"github.com/LiterMC/go-openbmclapi/storage"
	"github.com/LiterMC/go-openbmclapi/utils"
)

type Cluster struct {
	host          string   // not the public access host, but maybe a public IP, or a host that will be resolved to the IP
	publicHosts   []string // should not contains port, can be nil
	publicPort    uint16
	clusterId     string
	clusterSecret string
	prefix        string
	byoc          bool
	jwtIssuer     string

	dataDir            string
	maxConn            int
	storageOpts        []storage.StorageOption
	storages           []storage.Storage
	storageWeights     []uint
	storageTotalWeight uint
	cache              gocache.Cache
	apiHmacKey         []byte
	hijackProxy        *HjProxy

	stats                  notify.Stats
	lastHits, statOnlyHits atomic.Int32
	lastHbts, statOnlyHbts atomic.Int64
	issync                 atomic.Bool
	syncProg               atomic.Int64
	syncTotal              atomic.Int64

	mux             sync.RWMutex
	enabled         atomic.Bool
	disabled        chan struct{}
	waitEnable      []chan struct{}
	shouldEnable    atomic.Bool
	reconnectCount  int
	socket          *socket.Socket
	cancelKeepalive context.CancelFunc
	downloadMux     sync.RWMutex
	downloading     map[string]*downloadingItem
	filesetMux      sync.RWMutex
	fileset         map[string]int64
	authTokenMux    sync.RWMutex
	authToken       *ClusterToken

	client         *http.Client
	cachedCli      *http.Client
	bufSlots       *limited.BufSlots
	database       database.DB
	notifyManager  *notify.Manager
	webpushKeyB64  string
	updateChecker  *time.Ticker
	apiRateLimiter *limited.APIRateMiddleWare

	wsUpgrader    *websocket.Upgrader
	handlerAPIv0  http.Handler
	handlerAPIv1  http.Handler
	hijackHandler http.Handler
}

func NewCluster(
	ctx context.Context,
	prefix string,
	baseDir string,
	host string, publicPort uint16,
	clusterId string, clusterSecret string,
	byoc bool, dialer *net.Dialer,
	storageOpts []storage.StorageOption,
	cache gocache.Cache,
) (cr *Cluster) {
	transport := http.DefaultTransport
	if dialer != nil {
		transport = &http.Transport{
			DialContext: dialer.DialContext,
		}
	}

	cachedTransport := transport
	if cache != gocache.NoCache {
		cachedTransport = &httpcache.Transport{
			Transport: transport,
			Cache:     gocache.WrapToHTTPCache(gocache.NewCacheWithNamespace(cache, "http@")),
		}
	}

	cr = &Cluster{
		host:          host,
		publicPort:    publicPort,
		clusterId:     clusterId,
		clusterSecret: clusterSecret,
		prefix:        prefix,
		byoc:          byoc,
		jwtIssuer:     jwtIssuerPrefix + "#" + clusterId,

		dataDir:     filepath.Join(baseDir, "data"),
		maxConn:     config.DownloadMaxConn,
		storageOpts: storageOpts,
		cache:       cache,

		disabled: make(chan struct{}, 0),
		fileset:  make(map[string]int64, 0),

		downloading: make(map[string]*downloadingItem),

		client: &http.Client{
			Transport: transport,
		},
		cachedCli: &http.Client{
			Transport: cachedTransport,
		},

		wsUpgrader: &websocket.Upgrader{
			HandshakeTimeout: time.Minute,
		},
	}
	close(cr.disabled)

	if cr.maxConn <= 0 {
		panic("download-max-conn must be a positive integer")
	}
	cr.bufSlots = limited.NewBufSlots(cr.maxConn)

	{
		var (
			n   uint = 0
			wgs      = make([]uint, len(storageOpts))
			sts      = make([]storage.Storage, len(storageOpts))
		)
		for i, s := range storageOpts {
			sts[i] = storage.NewStorage(s)
			wgs[i] = s.Weight
			n += s.Weight
		}
		cr.storages = sts
		cr.storageWeights = wgs
		cr.storageTotalWeight = n
	}
	return
}

func (cr *Cluster) Init(ctx context.Context) (err error) {
	// create data folder
	os.MkdirAll(cr.dataDir, 0755)

	if config.Database.Driver == "memory" {
		cr.database = database.NewMemoryDB()
	} else if cr.database, err = database.NewSqlDB(config.Database.Driver, config.Database.DSN); err != nil {
		return
	}

	if config.Hijack.Enable {
		cr.hijackProxy = NewHjProxy(cr.client, cr.database, cr.handleDownload)
		if config.Hijack.EnableLocalCache {
			os.MkdirAll(config.Hijack.LocalCachePath, 0755)
		}
	}

	// Init notification manager
	cr.notifyManager = notify.NewManager(cr.dataDir, cr.database, cr.client, config.Dashboard.NotifySubject)
	// Add notification plugins
	webpushPlg := new(webpush.Plugin)
	cr.notifyManager.AddPlugin(webpushPlg)
	if config.Notification.EnableEmail {
		emailPlg, err := email.NewSMTP(
			config.Notification.EmailSMTP, config.Notification.EmailSMTPEncryption,
			config.Notification.EmailSender, config.Notification.EmailSenderPassword,
		)
		if err != nil {
			return err
		}
		cr.notifyManager.AddPlugin(emailPlg)
	}

	if err = cr.notifyManager.Init(ctx); err != nil {
		return
	}
	cr.webpushKeyB64 = base64.RawURLEncoding.EncodeToString(webpushPlg.GetPublicKey())

	// Init storages
	vctx := context.WithValue(ctx, storage.ClusterCacheCtxKey, cr.cache)
	for _, s := range cr.storages {
		s.Init(vctx)
	}

	// read old stats
	if err := cr.stats.Load(cr.dataDir); err != nil {
		log.Errorf("Could not load stats: %v", err)
	}
	if cr.apiHmacKey, err = utils.LoadOrCreateHmacKey(cr.dataDir); err != nil {
		return fmt.Errorf("Cannot load hmac key: %w", err)
	}

	cr.updateChecker = time.NewTicker(time.Hour)

	go func(ticker *time.Ticker) {
		defer log.RecoverPanic(nil)
		defer ticker.Stop()

		if err := cr.checkUpdate(); err != nil {
			log.TrErrorf("error.update.check.failed", err)
		}
		for range ticker.C {
			if err := cr.checkUpdate(); err != nil {
				log.TrErrorf("error.update.check.failed", err)
			}
		}
	}(cr.updateChecker)
	return
}

func (cr *Cluster) Destroy(ctx context.Context) {
	if cr.database != nil {
		cr.database.Cleanup()
	}
	cr.updateChecker.Stop()
	if cr.apiRateLimiter != nil {
		cr.apiRateLimiter.Destroy()
	}
}

func (cr *Cluster) allocBuf(ctx context.Context) (slotId int, buf []byte, free func()) {
	return cr.bufSlots.Alloc(ctx)
}

func (cr *Cluster) Connect(ctx context.Context) bool {
	cr.mux.Lock()
	defer cr.mux.Unlock()

	if cr.socket != nil {
		log.Debug("Extra connect")
		return true
	}

	_, err := cr.GetAuthToken(ctx)
	if err != nil {
		log.TrErrorf("error.cluster.auth.failed", err)
		osExit(CodeClientOrServerError)
	}

	engio, err := engine.NewSocket(engine.Options{
		Host: cr.prefix,
		Path: "/socket.io/",
		ExtraHeaders: http.Header{
			"Origin":     {cr.prefix},
			"User-Agent": {build.ClusterUserAgent},
		},
		DialTimeout: time.Minute * 6,
	})
	if err != nil {
		log.Errorf("Could not parse Engine.IO options: %v; exit.", err)
		osExit(CodeClientUnexpectedError)
	}

	cr.reconnectCount = 0
	connected := false

	if config.Advanced.SocketIOLog {
		engio.OnRecv(func(_ *engine.Socket, data []byte) {
			log.Debugf("Engine.IO recv: %q", (string)(data))
		})
		engio.OnSend(func(_ *engine.Socket, data []byte) {
			log.Debugf("Engine.IO sending: %q", (string)(data))
		})
	}
	engio.OnConnect(func(*engine.Socket) {
		log.Info("Engine.IO connected")
	})
	engio.OnDisconnect(func(_ *engine.Socket, err error) {
		if ctx.Err() != nil {
			// Ignore if the error is because context cancelled
			return
		}
		if err != nil {
			log.Warnf("Engine.IO disconnected: %v", err)
		}
		if config.MaxReconnectCount == 0 {
			if cr.shouldEnable.Load() {
				log.Errorf("Cluster disconnected from remote; exit.")
				osExit(CodeServerOrEnvionmentError)
			}
		}
		if !connected {
			cr.reconnectCount++
			if config.MaxReconnectCount > 0 && cr.reconnectCount >= config.MaxReconnectCount {
				if cr.shouldEnable.Load() {
					log.TrErrorf("error.cluster.connect.failed.toomuch")
					osExit(CodeServerOrEnvionmentError)
				}
			}
		}
		connected = false
		go cr.disconnected()
	})
	engio.OnDialError(func(_ *engine.Socket, err error) {
		cr.reconnectCount++
		log.TrErrorf("error.cluster.connect.failed", cr.reconnectCount, config.MaxReconnectCount, err)
		if config.MaxReconnectCount >= 0 && cr.reconnectCount >= config.MaxReconnectCount {
			if cr.shouldEnable.Load() {
				log.TrErrorf("error.cluster.connect.failed.toomuch")
				osExit(CodeServerOrEnvionmentError)
			}
		}
	})

	cr.socket = socket.NewSocket(engio, socket.WithAuthTokenFn(func() string {
		token, err := cr.GetAuthToken(ctx)
		if err != nil {
			log.TrErrorf("error.cluster.auth.failed", err)
			osExit(CodeServerOrEnvionmentError)
		}
		return token
	}))
	cr.socket.OnBeforeConnect(func(*socket.Socket) {
		log.Infof(Tr("info.cluster.connect.prepare"), cr.reconnectCount, config.MaxReconnectCount)
	})
	cr.socket.OnConnect(func(*socket.Socket, string) {
		connected = true
		log.Debugf("shouldEnable is %v", cr.shouldEnable.Load())
		if cr.shouldEnable.Load() {
			if err := cr.Enable(ctx); err != nil {
				log.TrErrorf("error.cluster.enable.failed", err)
				osExit(CodeClientOrEnvionmentError)
			}
		}
	})
	cr.socket.OnDisconnect(func(*socket.Socket, string) {
		go cr.disconnected()
	})
	cr.socket.OnError(func(_ *socket.Socket, err error) {
		if ctx.Err() != nil {
			// Ignore if the error is because context cancelled
			return
		}
		log.Errorf("Socket.IO error: %v", err)
	})
	cr.socket.OnMessage(func(event string, data []any) {
		if event == "message" {
			log.Infof("[remote]: %v", data[0])
		}
	})
	log.Infof("Dialing %s", engio.URL().String())
	if err := engio.Dial(ctx); err != nil {
		log.Errorf("Dial error: %v", err)
		return false
	}
	log.Info("Connecting to socket.io namespace")
	if err := cr.socket.Connect(""); err != nil {
		log.Errorf("Open namespace error: %v", err)
		return false
	}
	return true
}

func (cr *Cluster) disconnected() bool {
	cr.mux.Lock()
	defer cr.mux.Unlock()

	if cr.enabled.CompareAndSwap(true, false) {
		return false
	}
	if cr.cancelKeepalive != nil {
		cr.cancelKeepalive()
		cr.cancelKeepalive = nil
	}
	cr.notifyManager.OnDisabled()
	return true
}

func (cr *Cluster) disable(ctx context.Context) (ok bool) {
	cr.mux.Lock()
	defer cr.mux.Unlock()

	if !cr.enabled.Load() {
		log.Debug("Extra disable")
		return false
	}

	defer cr.notifyManager.OnDisabled()

	if cr.cancelKeepalive != nil {
		cr.cancelKeepalive()
		cr.cancelKeepalive = nil
	}
	if cr.socket == nil {
		return false
	}
	log.Info(Tr("info.cluster.disabling"))
	resCh, err := cr.socket.EmitWithAck("disable", nil)
	if err == nil {
		tctx, cancel := context.WithTimeout(ctx, time.Second*(time.Duration)(config.Advanced.KeepaliveTimeout))
		select {
		case <-tctx.Done():
			cancel()
			err = tctx.Err()
		case data := <-resCh:
			cancel()
			log.Debug("disable ack:", data)
			if ero := data[0]; ero != nil {
				log.Errorf("Disable failed: %v", ero)
			} else if !data[1].(bool) {
				log.Error("Disable failed: acked non true value")
			} else {
				ok = true
			}
		}
	}
	if err != nil {
		log.Errorf(Tr("error.cluster.disable.failed"), err)
	}

	cr.enabled.Store(false)
	go cr.socket.Close()
	cr.socket = nil
	close(cr.disabled)
	log.Warn(Tr("warn.cluster.disabled"))
	return
}
