/**
 * OpenBmclAPI (Golang Edition)
 * Copyright (C) 2024 Kevin Z <zyxkad@gmail.com>
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

package cluster

import (
	"net/http"
)

func (cr *Cluster) HandleFile(req *http.Request, rw http.ResponseWriter, hash string) {
	if cr.storageManager.ForEachFromRandom(cr.storages, func(s storage.Storage) bool {
		log.Debugf("[handler]: Checking %s on storage [%d] %s ...", hash, i, sto.String())

		sz, er := sto.ServeDownload(rw, req, hash, size)
		if er != nil {
			log.Debugf("[handler]: File %s failed on storage [%d] %s: %v", hash, i, sto.String(), er)
			err = er
			return false
		}
		if sz >= 0 {
			opts := cr.storageOpts[i]
			cr.AddHits(1, sz, s.Options().Id)
			if !keepaliveRec {
				cr.statOnlyHits.Add(1)
				cr.statOnlyHbts.Add(sz)
			}
		}
		return true
	}) {
		return
	}
	http.Error(http.StatusInternation)
}
