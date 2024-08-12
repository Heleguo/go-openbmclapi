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

package cluster_test

import (
	"testing"

	"io"
	"os"
)

var datas = func() [][]byte {
	datas := make([][]byte, 0x7)
	for i := range len(datas) {
		b := make([]byte, 0xff00+i)
		for j := range len(b) {
			b[j] = (byte)(i + j)
		}
		datas[i] = b
	}
	return datas
}()

func BenchmarkCreateAndRemoveFile(t *testing.B) {
	t.ReportAllocs()
	buf := make([]byte, 1024)
	_ = buf
	for i := 0; i < t.N; i++ {
		d := datas[i%len(datas)]
		fd, err := os.CreateTemp("", "*.downloading")
		if err != nil {
			t.Fatalf("Cannot create temp file: %v", err)
		}
		if _, err = fd.Write(d); err != nil {
			t.Errorf("Cannot write file: %v", err)
		} else if err = fd.Sync(); err != nil {
			t.Errorf("Cannot write file: %v", err)
		}
		fd.Close()
		os.Remove(fd.Name())
		if err != nil {
			t.FailNow()
		}
	}
}

func BenchmarkWriteAndTruncateFile(t *testing.B) {
	t.ReportAllocs()
	buf := make([]byte, 1024)
	_ = buf
	fd, err := os.CreateTemp("", "*.downloading")
	if err != nil {
		t.Fatalf("Cannot create temp file: %v", err)
	}
	defer os.Remove(fd.Name())
	for i := 0; i < t.N; i++ {
		d := datas[i%len(datas)]
		if _, err := fd.Write(d); err != nil {
			t.Fatalf("Cannot write file: %v", err)
		} else if err := fd.Sync(); err != nil {
			t.Fatalf("Cannot write file: %v", err)
		} else if err := fd.Truncate(0); err != nil {
			t.Fatalf("Cannot truncate file: %v", err)
		}
	}
}

func BenchmarkWriteAndSeekFile(t *testing.B) {
	t.ReportAllocs()
	buf := make([]byte, 1024)
	_ = buf
	fd, err := os.CreateTemp("", "*.downloading")
	if err != nil {
		t.Fatalf("Cannot create temp file: %v", err)
	}
	defer os.Remove(fd.Name())
	for i := 0; i < t.N; i++ {
		d := datas[i%len(datas)]
		if _, err := fd.Write(d); err != nil {
			t.Fatalf("Cannot write file: %v", err)
		} else if err := fd.Sync(); err != nil {
			t.Fatalf("Cannot write file: %v", err)
		} else if _, err := fd.Seek(io.SeekStart, 0); err != nil {
			t.Fatalf("Cannot seek file: %v", err)
		}
	}
}

func BenchmarkParallelCreateAndRemoveFile(t *testing.B) {
	t.ReportAllocs()
	t.SetParallelism(4)
	buf := make([]byte, 1024)
	_ = buf
	t.RunParallel(func(pb *testing.PB) {
		for i := 0; pb.Next(); i++ {
			d := datas[i%len(datas)]
			fd, err := os.CreateTemp("", "*.downloading")
			if err != nil {
				t.Fatalf("Cannot create temp file: %v", err)
			}
			if _, err = fd.Write(d); err != nil {
				t.Errorf("Cannot write file: %v", err)
			} else if err = fd.Sync(); err != nil {
				t.Errorf("Cannot write file: %v", err)
			}
			fd.Close()
			if err := os.Remove(fd.Name()); err != nil {
				t.Fatalf("Cannot remove file: %v", err)
			}
			if err != nil {
				t.FailNow()
			}
		}
	})
}

func BenchmarkParallelWriteAndTruncateFile(t *testing.B) {
	t.ReportAllocs()
	t.SetParallelism(4)
	buf := make([]byte, 1024)
	_ = buf
	t.RunParallel(func(pb *testing.PB) {
		fd, err := os.CreateTemp("", "*.downloading")
		if err != nil {
			t.Fatalf("Cannot create temp file: %v", err)
		}
		defer os.Remove(fd.Name())
		for i := 0; pb.Next(); i++ {
			d := datas[i%len(datas)]
			if _, err := fd.Write(d); err != nil {
				t.Fatalf("Cannot write file: %v", err)
			} else if err := fd.Sync(); err != nil {
				t.Fatalf("Cannot write file: %v", err)
			} else if err := fd.Truncate(0); err != nil {
				t.Fatalf("Cannot truncate file: %v", err)
			}
		}
	})
}

func BenchmarkParallelWriteAndSeekFile(t *testing.B) {
	t.ReportAllocs()
	t.SetParallelism(4)
	buf := make([]byte, 1024)
	_ = buf
	t.RunParallel(func(pb *testing.PB) {
		fd, err := os.CreateTemp("", "*.downloading")
		if err != nil {
			t.Fatalf("Cannot create temp file: %v", err)
		}
		defer os.Remove(fd.Name())
		for i := 0; pb.Next(); i++ {
			d := datas[i%len(datas)]
			if _, err := fd.Write(d); err != nil {
				t.Fatalf("Cannot write file: %v", err)
			} else if err := fd.Sync(); err != nil {
				t.Fatalf("Cannot write file: %v", err)
			} else if _, err := fd.Seek(io.SeekStart, 0); err != nil {
				t.Fatalf("Cannot seel file: %v", err)
			}
		}
	})
}
