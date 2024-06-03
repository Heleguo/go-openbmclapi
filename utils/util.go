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

package utils

import (
	"math/rand"
	"time"
	"errors"
	"os"
	"path/filepath"
	"sync"
)

type SyncMap[K comparable, V any] struct {
	l sync.RWMutex
	m map[K]V
}

func NewSyncMap[K comparable, V any]() *SyncMap[K, V] {
	return &SyncMap[K, V]{
		m: make(map[K]V),
	}
}

func (m *SyncMap[K, V]) Len() int {
	m.l.RLock()
	defer m.l.RUnlock()
	return len(m.m)
}

func (m *SyncMap[K, V]) RawMap() map[K]V {
	return m.m
}

func (m *SyncMap[K, V]) Set(k K, v V) {
	m.l.Lock()
	defer m.l.Unlock()
	m.m[k] = v
}

func (m *SyncMap[K, V]) Get(k K) V {
	m.l.RLock()
	defer m.l.RUnlock()
	return m.m[k]
}

func (m *SyncMap[K, V]) Has(k K) bool {
	m.l.RLock()
	defer m.l.RUnlock()
	_, ok := m.m[k]
	return ok
}

func (m *SyncMap[K, V]) GetOrSet(k K, setter func() V) (v V, has bool) {
	m.l.RLock()
	v, has = m.m[k]
	m.l.RUnlock()
	if has {
		return
	}
	m.l.Lock()
	defer m.l.Unlock()
	v, has = m.m[k]
	if !has {
		v = setter()
		m.m[k] = v
	}
	return
}

func WalkCacheDir(cacheDir string, walker func(hash string, size int64) (err error)) (err error) {
	for _, dir := range Hex256 {
		files, err := os.ReadDir(filepath.Join(cacheDir, dir))
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return err
		}
		for _, f := range files {
			if !f.IsDir() {
				if hash := f.Name(); len(hash) >= 2 && hash[:2] == dir {
					if info, err := f.Info(); err == nil {
						if err := walker(hash, info.Size()); err != nil {
							return err
						}
					}
				}
			}
		}
	}
	return nil
}

var rd = func() chan int32 {
	ch := make(chan int32, 64)
	r := rand.New(rand.NewSource(time.Now().Unix()))
	go func() {
		for {
			ch <- r.Int31()
		}
	}()
	return ch
}()

func RandIntn(n int) int {
	rn := <-rd
	return (int)(rn) % n
}

func ForEachFromRandomIndex(leng int, cb func(i int) (done bool)) (done bool) {
	if leng <= 0 {
		return false
	}
	start := RandIntn(leng)
	for i := start; i < leng; i++ {
		if cb(i) {
			return true
		}
	}
	for i := 0; i < start; i++ {
		if cb(i) {
			return true
		}
	}
	return false
}

func ForEachFromRandomIndexWithPossibility(poss []uint, total uint, cb func(i int) (done bool)) (done bool) {
	leng := len(poss)
	if leng == 0 {
		return false
	}
	if total == 0 {
		return ForEachFromRandomIndex(leng, cb)
	}
	n := (uint)(RandIntn((int)(total)))
	start := 0
	for i, p := range poss {
		if n < p {
			start = i
			break
		}
		n -= p
	}
	for i := start; i < leng; i++ {
		if cb(i) {
			return true
		}
	}
	for i := 0; i < start; i++ {
		if cb(i) {
			return true
		}
	}
	return false
}
