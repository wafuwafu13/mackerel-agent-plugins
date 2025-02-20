package mpsidekiq

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	r "github.com/go-redis/redis"
	mp "github.com/mackerelio/go-mackerel-plugin-helper"
)

// SidekiqPlugin for fetching metrics
type SidekiqPlugin struct {
	Client    *r.Client
	Namespace string
	Prefix    string
}

var graphdef = map[string]mp.Graphs{
	"ProcessedANDFailed": mp.Graphs{
		Label: "Sidekiq processed and failed count",
		Unit:  "integer",
		Metrics: []mp.Metrics{
			{Name: "processed", Label: "Processed", Type: "uint64", Diff: true},
			{Name: "failed", Label: "Failed", Type: "uint64", Diff: true},
		},
	},
	"Stats": mp.Graphs{
		Label: "Sidekiq stats",
		Unit:  "integer",
		Metrics: []mp.Metrics{
			{Name: "busy", Label: "Busy", Type: "uint64"},
			{Name: "enqueued", Label: "Enqueued", Type: "uint64"},
			{Name: "schedule", Label: "Schedule", Type: "uint64"},
			{Name: "retry", Label: "Retry", Type: "uint64"},
			{Name: "dead", Label: "Dead", Type: "uint64"},
		},
	},
	"QueueLatency": mp.Graphs{
		Label: "Sidekiq queue latency",
		Unit:  "float",
		Metrics: []mp.Metrics{
			{Name: "*", Label: "%1"},
		},
	},
}

// GraphDefinition Graph definition
func (sp SidekiqPlugin) GraphDefinition() map[string]mp.Graphs {
	return graphdef
}

func (sp SidekiqPlugin) get(key string) uint64 {
	val, err := sp.Client.Get(key).Result()
	if err == r.Nil {
		return 0
	}

	r, _ := strconv.ParseUint(val, 10, 64)
	return r
}

func (sp SidekiqPlugin) zCard(key string) uint64 {
	val, err := sp.Client.ZCard(key).Result()
	if err == r.Nil {
		return 0
	}

	return uint64(val)
}

func (sp SidekiqPlugin) sMembers(key string) []string {
	val, err := sp.Client.SMembers(key).Result()
	if err == r.Nil {
		return make([]string, 0)
	}

	return val
}

func (sp SidekiqPlugin) hGet(key string, field string) uint64 {
	val, err := sp.Client.HGet(key, field).Result()
	if err == r.Nil {
		return 0
	}

	r, _ := strconv.ParseUint(val, 10, 64)
	return r
}

func (sp SidekiqPlugin) lLen(key string) uint64 {
	val, err := sp.Client.LLen(key).Result()
	if err == r.Nil {
		return 0
	}

	return uint64(val)
}

func addNamespace(namespace string, key string) string {
	if namespace == "" {
		return key
	}
	return namespace + ":" + key
}

func (sp SidekiqPlugin) getProcessed() uint64 {
	key := addNamespace(sp.Namespace, "stat:processed")
	return sp.get(key)
}

func (sp SidekiqPlugin) getFailed() uint64 {
	key := addNamespace(sp.Namespace, "stat:failed")
	return sp.get(key)
}

func inject(slice []uint64, base uint64) uint64 {
	for _, e := range slice {
		base += uint64(e)
	}

	return base
}

func (sp SidekiqPlugin) getBusy() uint64 {
	key := addNamespace(sp.Namespace, "processes")
	processes := sp.sMembers(key)
	busies := make([]uint64, 10)
	for _, e := range processes {
		e := addNamespace(sp.Namespace, e)
		busies = append(busies, sp.hGet(e, "busy"))
	}

	return inject(busies, 0)
}

func (sp SidekiqPlugin) getEnqueued() uint64 {
	key := addNamespace(sp.Namespace, "queues")
	queues := sp.sMembers(key)
	queuesLlens := make([]uint64, 10)

	prefix := addNamespace(sp.Namespace, "queue:")
	for _, e := range queues {
		queuesLlens = append(queuesLlens, sp.lLen(prefix+e))
	}

	return inject(queuesLlens, 0)
}

func (sp SidekiqPlugin) getSchedule() uint64 {
	key := addNamespace(sp.Namespace, "schedule")
	return sp.zCard(key)
}

func (sp SidekiqPlugin) getRetry() uint64 {
	key := addNamespace(sp.Namespace, "retry")
	return sp.zCard(key)
}

func (sp SidekiqPlugin) getDead() uint64 {
	key := addNamespace(sp.Namespace, "dead")
	return sp.zCard(key)
}

func (sp SidekiqPlugin) getProcessedFailed() map[string]interface{} {
	data := make(map[string]interface{}, 20)

	data["processed"] = sp.getProcessed()
	data["failed"] = sp.getFailed()

	return data
}

func (sp SidekiqPlugin) getStats(field []string) map[string]interface{} {
	stats := make(map[string]interface{}, 20)
	for _, e := range field {
		switch e {
		case "busy":
			stats[e] = sp.getBusy()
		case "enqueued":
			stats[e] = sp.getEnqueued()
		case "schedule":
			stats[e] = sp.getSchedule()
		case "retry":
			stats[e] = sp.getRetry()
		case "dead":
			stats[e] = sp.getDead()
		}
	}

	return stats
}

func metricName(names ...string) string {
	return strings.Join(names, ".")
}

func (sp SidekiqPlugin) getQueueLatency() map[string]interface{} {
	latency := make(map[string]interface{}, 10)

	key := addNamespace(sp.Namespace, "queues")
	queues := sp.sMembers(key)

	prefix := addNamespace(sp.Namespace, "queue:")
	for _, q := range queues {
		queuesLRange, err := sp.Client.LRange(prefix+q, -1, -1).Result()
		if err != nil {
			fmt.Fprintf(os.Stderr, "get last queue error")
		}

		if len(queuesLRange) == 0 {
			latency[metricName("QueueLatency", q)] = 0.0
			continue
		}
		var job map[string]interface{}
		var thence float64

		err = json.Unmarshal([]byte(queuesLRange[0]), &job)
		if err != nil {
			fmt.Fprintf(os.Stderr, "json parse error")
			continue
		}
		now := float64(time.Now().Unix())
		if enqueuedAt, ok := job["enqueued_at"]; ok {
			enqueuedAt := enqueuedAt.(float64)
			thence = enqueuedAt
		} else {
			thence = now
		}
		latency[metricName("QueueLatency", q)] = (now - thence)
	}

	return latency
}

// FetchMetrics fetch the metrics
func (sp SidekiqPlugin) FetchMetrics() (map[string]interface{}, error) {
	field := []string{"busy", "enqueued", "schedule", "retry", "dead", "latency"}
	stats := sp.getStats(field)
	pf := sp.getProcessedFailed()
	latency := sp.getQueueLatency()

	// merge maps
	m := func(m ...map[string]interface{}) map[string]interface{} {
		r := make(map[string]interface{}, 20)
		for _, c := range m {
			for k, v := range c {
				r[k] = v
			}
		}

		return r
	}(stats, pf, latency)

	return m, nil
}

// MetricKeyPrefix interface for PluginWithPrefix
func (sp SidekiqPlugin) MetricKeyPrefix() string {
	if sp.Prefix == "" {
		sp.Prefix = "sidekiq"
	}
	return sp.Prefix
}

// Do the plugin
func Do() {
	optHost := flag.String("host", "localhost", "Hostname")
	optPort := flag.String("port", "6379", "Port")
	optPassword := flag.String("password", os.Getenv("SIDEKIQ_PASSWORD"), "Password")
	optDB := flag.Int("db", 0, "DB")
	optNamespace := flag.String("redis-namespace", "", "Redis namespace")
	optPrefix := flag.String("metric-key-prefix", "sidekiq", "Metric key prefix")
	optTempfile := flag.String("tempfile", "", "Temp file name")
	flag.Parse()

	client := r.NewClient(&r.Options{
		Addr:     fmt.Sprintf("%s:%s", *optHost, *optPort),
		Password: *optPassword,
		DB:       *optDB,
	})

	sp := SidekiqPlugin{
		Client:    client,
		Namespace: *optNamespace,
		Prefix:    *optPrefix,
	}
	helper := mp.NewMackerelPlugin(sp)
	helper.Tempfile = *optTempfile

	helper.Run()
}
