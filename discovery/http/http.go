// Copyright 2017 The Prometheus Authors
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

package http

import (
	"errors"
	"fmt"
	"io/ioutil"
	net_http "net/http"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/common/log"
	"github.com/prometheus/common/model"
	"golang.org/x/net/context"

	"github.com/prometheus/prometheus/config"
	"github.com/prometheus/prometheus/util/httputil"
)

const (
	// Constants for instrumentation.
	namespace = "prometheus"
)

var (
	httpSDTargetsTotal = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "prometheus_sd_http_targets_total",
			Help:      "The number of Http-SD targets total.",
		})
	httpSDRefreshFailureCount = prometheus.NewCounter(
		prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "prometheus_sd_http_refresh_failure_count",
			Help:      "The number of Http-SD refresh failures.",
		})
	httpSDrefreshDuration = prometheus.NewSummary(
		prometheus.SummaryOpts{
			Namespace: namespace,
			Name:      "prometheus_sd_http_refresh_duration_seconds",
			Help:      "The duration of a Http-SD refresh in seconds.",
		})
)

func init() {
	prometheus.MustRegister(httpSDTargetsTotal)
	prometheus.MustRegister(httpSDRefreshFailureCount)
	prometheus.MustRegister(httpSDrefreshDuration)
}

// Discovery periodically performs Http-SD requests. It implements
// the TargetProvider interface.
type Discovery struct {
	url             string
	method          string
	headers         map[string]string
	body            string
	client          *net_http.Client
	refreshInterval time.Duration
}

// NewDiscovery returns a new Discovery which periodically refreshes its targets.
func NewDiscovery(conf *config.HttpSDConfig) (*Discovery, error) {
	tls, err := httputil.NewTLSConfig(conf.TLSConfig)
	if err != nil {
		return nil, err
	}

	client := &net_http.Client{
		Timeout: time.Duration(conf.Timeout),
		Transport: &net_http.Transport{
			TLSClientConfig: tls,
		},
	}

	return &Discovery{
		client:          client,
		url:             conf.Url,
		method:          conf.Method,
		headers:         conf.Headers,
		body:            conf.Body,
		refreshInterval: time.Duration(conf.RefreshInterval),
	}, nil
}

// Run implements the TargetProvider interface.
func (d *Discovery) Run(ctx context.Context, ch chan<- []*config.TargetGroup) {
	defer d.stop()
	d.refresh(ctx, ch)

	ticker := time.NewTicker(d.refreshInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.refresh(ctx, ch)
		}
	}
}

func (d *Discovery) stop() {
}

func (d *Discovery) doRequest() (map[string]*config.TargetGroup, error) {
	var request *net_http.Request
	var err error

	if d.method == "GET" || d.method == "POST" {
		request, err = net_http.NewRequest(d.method, d.url, nil)
		if err != nil {
			return nil, err
		}
	} else {
		return nil, errors.New(fmt.Sprintf("Unsupported request method %s", d.method))
	}

	for key, value := range d.headers {
		if strings.Title(key) == "Host" {
			request.Host = value
			continue
		}
		request.Header.Set(key, value)
	}

	if d.body != "" {
		request.Body = ioutil.NopCloser(strings.NewReader(d.body))
	}

	resp, err := d.client.Do(request)
	if err != nil || resp == nil {
		return nil, errors.New(fmt.Sprintf("Cannot get response from %s, err: %s", d.url, err))
	} else {
		log.Infof("Got response %s", resp)
		return nil, nil
	}
}

func (d *Discovery) doFakedRequest() ([]*config.TargetGroup, error) {
	var (
		tg           config.TargetGroup
		fakedTargets = []string{
			"localhost:9090",
			"example.org:443",
		}
		fakedTargetLabels = model.LabelSet{
			"foo": "bar",
		}
	)

	tg.Targets = make([]model.LabelSet, 0, len(fakedTargets))
	for t := range fakedTargets {
		tg.Targets = append(tg.Targets, model.LabelSet{
			model.AddressLabel: model.LabelValue(t),
		})
	}
	tg.Labels = fakedTargetLabels

	log.Infof("This is %v", tg)
	return []*config.TargetGroup{
		&tg,
	}, nil
}

// refresh call the Http server with specified params definied in discovery configs and sends the respective
// updated target groups through the channel.
func (d *Discovery) refresh(ctx context.Context, ch chan<- []*config.TargetGroup) {
	t0 := time.Now()
	defer func() {
		httpSDrefreshDuration.Observe(time.Since(t0).Seconds())
	}()

	// here we call the http server via discovery config
	tgroups, err := d.doFakedRequest()
	if err != nil {
		httpSDRefreshFailureCount.Inc()
		log.Errorf("Error requesting %s: %s", d.url, err)
	}

	log.Infof("Got target groups: %s", tgroups)

	select {
	case ch <- tgroups:
	case <-ctx.Done():
		return
	}
}
