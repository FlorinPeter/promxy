// Copyright 2016 The Prometheus Authors
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"bufio"
	"io/ioutil"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gogo/protobuf/proto"
	"github.com/golang/snappy"
	flags "github.com/jessevdk/go-flags"
	"github.com/prometheus/common/model"
	"github.com/sirupsen/logrus"

	"github.com/prometheus/prometheus/prompb"
)

var opts struct {
	BindAddr    string        `long:"bind-addr" description:"address to listen on" default:":8083"`
	WritePath   string        `long:"write-path" description:"url path" default:"/receive"`
	MetricsPath string        `long:"metrics-path" description:"url path" default:"/metrics"`
	TTL         time.Duration `long:"metric-ttl" description:"how long until we TTL things out of the map" required:"true"`
}

func main() {
	parser := flags.NewParser(&opts, flags.Default)
	if _, err := parser.Parse(); err != nil {
		// If the error was from the parser, then we can simply return
		// as Parse() prints the error already
		if _, ok := err.(*flags.Error); ok {
			os.Exit(1)
		}
		logrus.Fatalf("Error parsing flags: %v", err)
	}

	l := sync.Mutex{}
	m := make(map[string]*prompb.Sample)

	// TODO: ttl things
	go func() {
		for {
			time.Sleep(opts.TTL)
			cutoff := int64(model.Now().Add(-opts.TTL))

			l.Lock()
			for k, v := range m {
				if v.Timestamp < cutoff {
					delete(m, k)
				}
			}
			l.Unlock()
		}
	}()

	http.HandleFunc(opts.WritePath, func(w http.ResponseWriter, r *http.Request) {
		compressed, err := ioutil.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		reqBuf, err := snappy.Decode(nil, compressed)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		var req prompb.WriteRequest
		if err := proto.Unmarshal(reqBuf, &req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		for _, ts := range req.Timeseries {
			metric := make(model.Metric, len(ts.Labels))
			for _, l := range ts.Labels {
				metric[model.LabelName(l.Name)] = model.LabelValue(l.Value)
			}

			// pick which point
			var sample *prompb.Sample
			for _, s := range ts.Samples {
				if sample == nil {
					sample = s
					continue
				}
				if s.Timestamp > sample.Timestamp {
					sample = s
				}
			}

			l.Lock()
			m[metric.String()] = sample
			l.Unlock()
		}
	})

	http.HandleFunc(opts.MetricsPath, func(w http.ResponseWriter, r *http.Request) {
		writer := bufio.NewWriter(w)
		l.Lock()
		for k, v := range m {
			var sb strings.Builder

			sb.WriteString(k)
			sb.WriteRune(' ')

			sb.WriteString(strconv.FormatFloat(float64(v.Value), 'f', -1, 64))

			if v.Timestamp > 0 {
				sb.WriteRune(' ')
				sb.WriteString(strconv.FormatInt(v.Timestamp, 10))
			}
			sb.WriteByte('\n')

			writer.WriteString(sb.String())
		}
		l.Unlock()
		writer.Flush()
	})

	logrus.Fatal(http.ListenAndServe(opts.BindAddr, nil))
}
