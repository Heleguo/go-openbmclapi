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
	"bytes"
	"context"
	"crypto"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/textproto"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/LiterMC/go-openbmclapi/internal/build"
	"github.com/LiterMC/go-openbmclapi/limited"
	"github.com/LiterMC/go-openbmclapi/log"
	"github.com/LiterMC/go-openbmclapi/storage"
	"github.com/LiterMC/go-openbmclapi/utils"
)

func init() {
	// ignore TLS handshake error
	log.AddStdLogFilter(func(line []byte) bool {
		return bytes.HasPrefix(line, ([]byte)("http: TLS handshake error"))
	})
}

const (
	RealAddrCtxKey       = "handle.real.addr"
	RealPathCtxKey       = "handle.real.path"
	AccessLogExtraCtxKey = "handle.access.extra"
)

func GetRequestRealPath(req *http.Request) string {
	return req.Context().Value(RealPathCtxKey).(string)
}

func SetAccessInfo(req *http.Request, key string, value any) {
	if info, ok := req.Context().Value(AccessLogExtraCtxKey).(map[string]any); ok {
		info[key] = value
	}
}

type preAccessRecord struct {
	Type   string    `json:"type"`
	Time   time.Time `json:"time"`
	Addr   string    `json:"addr"`
	Method string    `json:"method"`
	URI    string    `json:"uri"`
	UA     string    `json:"ua"`
}

func (r *preAccessRecord) String() string {
	return fmt.Sprintf("Serving %-15s | %-4s %s | %q", r.Addr, r.Method, r.URI, r.UA)
}

type accessRecord struct {
	Type    string         `json:"type"`
	Status  int            `json:"status"`
	Used    time.Duration  `json:"used"`
	Content int64          `json:"content"`
	Addr    string         `json:"addr"`
	Proto   string         `json:"proto"`
	Method  string         `json:"method"`
	URI     string         `json:"uri"`
	UA      string         `json:"ua"`
	Extra   map[string]any `json:"extra,omitempty"`
}

func (r *accessRecord) String() string {
	used := r.Used
	if used > time.Minute {
		used = used.Truncate(time.Second)
	} else if used > time.Second {
		used = used.Truncate(time.Microsecond)
	}
	var buf strings.Builder
	fmt.Fprintf(&buf, "Serve %3d | %12v | %7s | %-15s | %-4s %s | %q",
		r.Status, used, utils.BytesToUnit((float64)(r.Content)),
		r.Addr,
		r.Method, r.URI, r.UA)
	if len(r.Extra) > 0 {
		buf.WriteString(" | ")
		e := json.NewEncoder(&utils.NoLastNewLineWriter{Writer: &buf})
		e.SetEscapeHTML(false)
		e.Encode(r.Extra)
	}
	return buf.String()
}

func (cr *Cluster) GetHandler() http.Handler {
	cr.apiRateLimiter = limited.NewAPIRateMiddleWare(RealAddrCtxKey, loggedUserKey)
	cr.apiRateLimiter.SetAnonymousRateLimit(config.RateLimit.Anonymous)
	cr.apiRateLimiter.SetLoggedRateLimit(config.RateLimit.Logged)
	cr.handlerAPIv0 = http.StripPrefix("/api/v0", cr.cliIdHandle(cr.initAPIv0()))
	cr.hijackHandler = http.StripPrefix("/bmclapi", cr.hijackProxy)

	handler := utils.NewHttpMiddleWareHandler(cr)
	// recover panic and log it
	handler.UseFunc(func(rw http.ResponseWriter, req *http.Request, next http.Handler) {
		defer log.RecoverPanic(func(any) {
			rw.WriteHeader(http.StatusInternalServerError)
		})
		next.ServeHTTP(rw, req)
	})

	if !config.Advanced.DoNotRedirectHTTPSToSecureHostname {
		// rediect the client to the first public host if it is connecting with a unsecure host
		handler.UseFunc(func(rw http.ResponseWriter, req *http.Request, next http.Handler) {
			host, _, err := net.SplitHostPort(req.Host)
			if err != nil {
				host = req.Host
			}
			if host != "" && len(cr.publicHosts) > 0 {
				host = strings.ToLower(host)
				needRed := true
				for _, h := range cr.publicHosts { // cr.publicHosts are already lower case
					if h, ok := strings.CutPrefix(h, "*."); ok {
						if strings.HasSuffix(host, h) {
							needRed = false
							break
						}
					} else if host == h {
						needRed = false
						break
					}
				}
				if needRed {
					host := ""
					for _, h := range cr.publicHosts {
						if !strings.HasSuffix(h, "*.") {
							host = h
							break
						}
					}
					if host != "" {
						u := *req.URL
						u.Scheme = "https"
						u.Host = net.JoinHostPort(host, strconv.Itoa((int)(cr.publicPort)))

						log.Debugf("Redirecting from %s to %s", req.Host, u.String())

						rw.Header().Set("Location", u.String())
						rw.Header().Set("Content-Length", "0")
						rw.WriteHeader(http.StatusFound)
						return
					}
				}
			}
			next.ServeHTTP(rw, req)
		})
	}

	handler.Use(cr.createRecordMiddleWare())
	return handler
}

func (cr *Cluster) createRecordMiddleWare() utils.MiddleWareFunc {
	type record struct {
		used   float64
		bytes  float64
		ua     string
		skipUA bool
	}
	recordCh := make(chan record, 1024)

	go func() {
		defer log.RecoverPanic(nil)

		updateTicker := time.NewTicker(time.Minute)
		defer updateTicker.Stop()

		var (
			total      int
			totalUsed  float64
			totalBytes float64
			uas        = make(map[string]int, 10)
		)
		for {
			select {
			case <-updateTicker.C:
				cr.stats.Lock()

				log.Infof("Served %d requests, total responsed body = %s, total used CPU time = %.2fs",
					total, utils.BytesToUnit(totalBytes), totalUsed)
				for ua, v := range uas {
					if ua == "" {
						ua = "[Unknown]"
					}
					cr.stats.Accesses[ua] += v
				}

				total = 0
				totalUsed = 0
				totalBytes = 0
				clear(uas)

				cr.stats.Unlock()
			case rec := <-recordCh:
				total++
				totalUsed += rec.used
				totalBytes += rec.bytes
				if !rec.skipUA {
					uas[rec.ua]++
				}
			}
		}
	}()

	return func(rw http.ResponseWriter, req *http.Request, next http.Handler) {
		ua := req.UserAgent()
		var addr string
		if config.TrustedXForwardedFor {
			// X-Forwarded-For: <client>, <proxy1>, <proxy2>
			adr, _, _ := strings.Cut(req.Header.Get("X-Forwarded-For"), ",")
			addr = strings.TrimSpace(adr)
		}
		if addr == "" {
			addr, _, _ = net.SplitHostPort(req.RemoteAddr)
		}
		srw := utils.WrapAsStatusResponseWriter(rw)
		start := time.Now()

		log.LogAccess(log.LevelDebug, &preAccessRecord{
			Type:   "pre-access",
			Time:   start,
			Addr:   addr,
			Method: req.Method,
			URI:    req.RequestURI,
			UA:     ua,
		})

		extraInfoMap := make(map[string]any)
		ctx := req.Context()
		ctx = context.WithValue(ctx, RealAddrCtxKey, addr)
		ctx = context.WithValue(ctx, RealPathCtxKey, req.URL.Path)
		ctx = context.WithValue(ctx, AccessLogExtraCtxKey, extraInfoMap)
		req = req.WithContext(ctx)
		next.ServeHTTP(srw, req)

		used := time.Since(start)
		accRec := &accessRecord{
			Type:    "access",
			Status:  srw.Status,
			Used:    used,
			Content: srw.Wrote,
			Addr:    addr,
			Proto:   req.Proto,
			Method:  req.Method,
			URI:     req.RequestURI,
			UA:      ua,
		}
		if len(extraInfoMap) > 0 {
			accRec.Extra = extraInfoMap
		}
		log.LogAccess(log.LevelInfo, accRec)

		if srw.Status < 200 || 400 <= srw.Status {
			return
		}
		if !strings.HasPrefix(req.URL.Path, "/download/") {
			return
		}
		var rec record
		rec.used = used.Seconds()
		rec.bytes = (float64)(srw.Wrote)
		ua, _, _ = strings.Cut(ua, " ")
		rec.ua, _, _ = strings.Cut(ua, "/")
		rec.skipUA = extraInfoMap["skip-ua-count"] != nil
		select {
		case recordCh <- rec:
		default:
		}
	}
}

func (cr *SubCluster) checkQuerySign(req *http.Request, hash string) bool {
	if config.Advanced.SkipSignatureCheck {
		return true
	}
	query := req.URL.Query()
	sign, e := query.Get("s"), query.Get("e")
	if len(sign) == 0 || len(e) == 0 {
		return false
	}
	before, err := strconv.ParseInt(e, 36, 64)
	if err != nil {
		return false
	}
	if time.Now().UnixMilli() > before {
		return false
	}
	hs := crypto.SHA1.New()
	io.WriteString(hs, cr.clusterSecret)
	io.WriteString(hs, hash)
	io.WriteString(hs, e)
	var (
		buf  [20]byte
		sbuf [27]byte
	)
	base64.RawURLEncoding.Encode(sbuf[:], hs.Sum(buf[:0]))
	if (string)(sbuf[:]) != sign {
		return false
	}
	return true
}

var emptyHashes = func() (hashes map[string]struct{}) {
	hashMethods := []crypto.Hash{
		crypto.MD5, crypto.SHA1,
	}
	hashes = make(map[string]struct{}, len(hashMethods))
	for _, h := range hashMethods {
		hs := hex.EncodeToString(h.New().Sum(nil))
		hashes[hs] = struct{}{}
	}
	return
}()

var HeaderXPoweredBy = fmt.Sprintf("go-openbmclapi/%s; url=https://github.com/LiterMC/go-openbmclapi", build.BuildVersion)

//go:embed robots.txt
var robotTxtContent string

func (cr *Cluster) ServeHTTP(rw http.ResponseWriter, req *http.Request) {
	method := req.Method
	u := req.URL

	rw.Header().Set("X-Powered-By", HeaderXPoweredBy)

	var subCluster *SubCluster = nil
	clusterName, hasCluster := cr.clusterHosts[u.Host]
	if hasCluster {
		subCluster = cr.clusters[clusterName]
	}
	rawpath := u.EscapedPath()
	switch {
	case strings.HasPrefix(rawpath, "/download/"):
		if !hasCluster {
			http.Error(rw, "Unexpected hostname", http.StatusForbidden)
			return
		}
		if method != http.MethodGet && method != http.MethodHead {
			rw.Header().Set("Allow", http.MethodGet+", "+http.MethodHead)
			http.Error(rw, "405 Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}

		hash := rawpath[len("/download/"):]
		if !utils.IsHex(hash) {
			http.Error(rw, hash+" is not a valid hash", http.StatusNotFound)
			return
		}

		if !subCluster.checkQuerySign(req, hash) {
			http.Error(rw, "Cannot verify signature", http.StatusForbidden)
			return
		}

		if !subCluster.shouldEnable.Load() {
			// do not serve file if cluster is not enabled yet
			http.Error(rw, "Cluster is not enabled yet", http.StatusServiceUnavailable)
			return
		}

		log.Debugf("Handling download %s", hash)
		subCluster.handleDownload(rw, req, hash)
		return
	case strings.HasPrefix(rawpath, "/measure/"):
		if !hasCluster {
			http.Error(rw, "Unexpected hostname", http.StatusForbidden)
			return
		}
		if method != http.MethodGet && method != http.MethodHead {
			rw.Header().Set("Allow", http.MethodGet+", "+http.MethodHead)
			http.Error(rw, "405 Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}

		if !subCluster.checkQuerySign(req, u.Path) {
			http.Error(rw, "Cannot verify signature", http.StatusForbidden)
			return
		}

		size := rawpath[len("/measure/"):]
		n, e := strconv.Atoi(size)
		if e != nil {
			http.Error(rw, e.Error(), http.StatusBadRequest)
			return
		} else if n < 0 || n > 200 {
			http.Error(rw, fmt.Sprintf("measure size %d out of range (0, 200]", n), http.StatusBadRequest)
			return
		}
		subCluster.storages.HandleMeasure(rw, req, n)
		return
	case strings.HasPrefix(rawpath, "/api/"):
		version, _, _ := strings.Cut(rawpath[len("/api/"):], "/")
		switch version {
		case "v0":
			cr.handlerAPIv0.ServeHTTP(rw, req)
			return
		}
	case rawpath == "/robots.txt":
		http.ServeContent(rw, req, "robots.txt", time.Time{}, strings.NewReader(robotTxtContent))
		return
	case strings.HasPrefix(rawpath, "/dashboard/"):
		if !config.Dashboard.Enable {
			http.NotFound(rw, req)
			return
		}
		pth := rawpath[len("/dashboard/"):]
		cr.serveDashboard(rw, req, pth)
		return
	case rawpath == "/" || rawpath == "/dashboard":
		http.Redirect(rw, req, "/dashboard/", http.StatusFound)
		return
	case strings.HasPrefix(rawpath, "/bmclapi/"):
		cr.hijackHandler.ServeHTTP(rw, req)
		return
	}
	http.NotFound(rw, req)
}

func (cr *SubCluster) handleDownload(rw http.ResponseWriter, req *http.Request, hash string) {
	keepaliveRec := req.Context().Value("go-openbmclapi.handler.no.record.for.keepalive") != true
	rw.Header().Set("X-Bmclapi-Hash", hash)

	if _, ok := emptyHashes[hash]; ok {
		name := req.URL.Query().Get("name")
		rw.Header().Set("ETag", `"`+hash+`"`)
		rw.Header().Set("Cache-Control", "public, max-age=31536000, immutable") // cache for a year
		rw.Header().Set("Content-Type", "application/octet-stream")
		rw.Header().Set("Content-Length", "0")
		if name != "" {
			rw.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name))
		}
		rw.WriteHeader(http.StatusOK)
		cr.root.stats.AddHits(1, 0, "")
		if keepaliveRec {
			cr.hits.Add(1)
		}
		return
	}

	if r := req.Header.Get("Range"); r != "" {
		if start, ok := parseRangeFirstStart(r); ok && start != 0 {
			SetAccessInfo(req, "skip-ua-count", "range")
		}
	}

	// check if file was indexed in the fileset
	size, ok := cr.CachedFileSize(hash)
	if !ok {
		if err := cr.root.DownloadFile(req.Context(), hash); err != nil {
			// TODO: check if the file exists
			http.Error(rw, "Cannot download file from center server: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}
	hits, hbts, name := cr.storages.HandleDownload(rw, req, hash, size)
	if hits > 0 {
		if keepaliveRec {
			cr.hits.Add(hits)
			cr.hbts.Add(hbts)
		}
	}
	if name != "" {
		SetAccessInfo(req, "storage", name)
	}
	log.Debug("[handler]: download served successed")
}

func (cr *Cluster) handleHijackDownload(rw http.ResponseWriter, req *http.Request, hash string) {

	if _, ok := emptyHashes[hash]; ok {
		name := req.URL.Query().Get("name")
		rw.Header().Set("X-Bmclapi-Hash", hash)
		rw.Header().Set("ETag", `"`+hash+`"`)
		rw.Header().Set("Cache-Control", "public, max-age=31536000, immutable") // cache for a year
		rw.Header().Set("Content-Type", "application/octet-stream")
		rw.Header().Set("Content-Length", "0")
		if name != "" {
			rw.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name))
		}
		rw.WriteHeader(http.StatusOK)
		cr.stats.AddHits(1, 0, "")
		return
	}

	if r := req.Header.Get("Range"); r != "" {
		if start, ok := parseRangeFirstStart(r); ok && start != 0 {
			SetAccessInfo(req, "skip-ua-count", "range")
		}
	}

	for _, subCluster := range cr.clusters {
		if _, ok := subCluster.CachedFileSize(hash); ok {
			subCluster.handleDownload(rw, req, hash)
			return
		}
	}
	http.Error(rw, "No sub cluster have the file", http.StatusNotFound)
}

// Note: this method is a fast parse, it does not deeply check if the Range is valid or not
func parseRangeFirstStart(rg string) (start int64, ok bool) {
	const b = "bytes="
	if rg, ok = strings.CutPrefix(rg, b); !ok {
		return
	}
	rg, _, _ = strings.Cut(rg, ",")
	var leng string
	if rg, leng, ok = strings.Cut(rg, "-"); !ok {
		return
	}
	if rg = textproto.TrimString(rg); rg == "" {
		return -1, true
	}
	if leng = textproto.TrimString(leng); leng == "" {
		return -1, true
	}
	start, err := strconv.ParseInt(rg, 10, 64)
	if err != nil {
		return 0, false
	}
	size, err := strconv.ParseInt(leng, 10, 64)
	if err != nil {
		return 0, false
	}
	if size == 0 {
		return -1, true
	}
	return start, true
}
