// Command loadgen measures HTTP board requests or database snapshots under
// concurrent polling. Run against synthetic data on a host you own.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"sort"
	"sync"
	"syscall"
	"time"

	"github.com/nicholashu/courtops/internal/store"
)

type options struct {
	mode, target, output                string
	viewers                             int
	eventID                             int64
	interval, duration, timeout, maxP95 time.Duration
	maxErrorRate                        float64
}

func main() {
	o := options{}
	flag.StringVar(&o.mode, "mode", "http", "http (board) or snapshot (database only)")
	flag.StringVar(&o.target, "url", "http://localhost:8080/board/1", "board URL to poll")
	flag.StringVar(&o.output, "json", "", "optional JSON report file")
	flag.IntVar(&o.viewers, "viewers", 30, "number of concurrent polling clients")
	flag.Int64Var(&o.eventID, "event", 1, "event ID for snapshot mode (DATABASE_URL required)")
	flag.DurationVar(&o.interval, "interval", 5*time.Second, "how often each client polls")
	flag.DurationVar(&o.duration, "duration", 60*time.Second, "how long to run")
	flag.DurationVar(&o.timeout, "timeout", 10*time.Second, "per-operation timeout")
	flag.DurationVar(&o.maxP95, "max-p95", 0, "fail above this p95; 0 disables the latency gate")
	flag.Float64Var(&o.maxErrorRate, "max-error-rate", 0, "maximum allowed failed fraction, from 0 to 1")
	flag.Parse()
	if err := run(o); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func (o options) validate() error {
	if o.viewers < 1 || o.interval <= 0 || o.duration <= 0 || o.timeout <= 0 || o.maxP95 < 0 {
		return errors.New("viewers, interval, duration and timeout must be positive; max-p95 must not be negative")
	}
	if math.IsNaN(o.maxErrorRate) || o.maxErrorRate < 0 || o.maxErrorRate > 1 {
		return errors.New("max-error-rate must be between 0 and 1")
	}
	if o.mode == "snapshot" {
		if o.eventID < 1 {
			return errors.New("event must be positive")
		}
		return nil
	}
	if o.mode != "http" {
		return errors.New("mode must be http or snapshot")
	}
	u, err := url.Parse(o.target)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("url must be an HTTP(S) URL without credentials, query parameters or fragment")
	}
	return nil
}

func run(o options) error {
	if err := o.validate(); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	c := &collector{}
	var operation func(context.Context)
	target := o.target
	if o.mode == "snapshot" {
		if os.Getenv("DATABASE_URL") == "" {
			return errors.New("snapshot mode requires DATABASE_URL")
		}
		connectCtx, cancel := context.WithTimeout(ctx, o.timeout)
		st, err := store.Open(connectCtx, os.Getenv("DATABASE_URL"))
		cancel()
		if err != nil {
			return errors.New("cannot connect to benchmark database (check DATABASE_URL)")
		}
		defer st.Close()
		target = fmt.Sprintf("database snapshot for event %d", o.eventID)
		operation = func(ctx context.Context) {
			started := time.Now()
			_, err := st.Snapshot(ctx, o.eventID)
			c.add(time.Since(started), err, 0)
		}
	} else {
		transport := &http.Transport{MaxIdleConns: o.viewers * 2, MaxIdleConnsPerHost: o.viewers * 2}
		defer transport.CloseIdleConnections()
		client := &http.Client{Timeout: o.timeout, Transport: transport,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		operation = func(ctx context.Context) { poll(ctx, client, o.target, c) }
	}
	fmt.Printf("%s: %s\n%d clients every %s (offered %.1f operations/second) for %s\n", o.mode, target, o.viewers, o.interval, float64(o.viewers)/o.interval.Seconds(), o.duration)
	// Stop scheduling at the deadline, then let in-flight requests finish within
	// their timeout. This avoids hiding slow operations cancelled at the cutoff.
	scheduleCtx, cancel := context.WithTimeout(ctx, o.duration)
	defer cancel()
	start := time.Now()
	var wg sync.WaitGroup
	for i := 0; i < o.viewers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			timer := time.NewTimer(time.Duration(rand.Int63n(int64(o.interval))))
			defer timer.Stop()
			for {
				select {
				case <-scheduleCtx.Done():
					return
				case <-timer.C:
				}
				if scheduleCtx.Err() != nil {
					return
				}
				opCtx, finish := context.WithTimeout(ctx, o.timeout)
				operation(opCtx)
				finish()
				// Closed-loop workload: a slow client cannot start overlapping requests.
				timer.Reset(o.interval)
			}
		}()
	}
	wg.Wait()
	r := c.report(time.Since(start))
	r.Mode, r.Target, r.Clients, r.IntervalMS = o.mode, target, o.viewers, float64(o.interval)/float64(time.Millisecond)
	r.StartedAt = start.UTC().Format(time.RFC3339)
	fmt.Printf("completed %d; successful %d; failed %d; %.2f operations/second\np50 %.3f ms; p95 %.3f ms; p99 %.3f ms; max %.3f ms\n", r.Requests, r.Successes, r.Errors, r.Throughput, r.P50MS, r.P95MS, r.P99MS, r.MaxMS)
	fmt.Printf("statuses %v\n", r.Statuses)
	if r.Successes < 1000 {
		fmt.Println("fewer than 1000 successful samples: p99 is exploratory")
	}
	if o.output != "" {
		data, err := json.MarshalIndent(r, "", "  ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(o.output, append(data, '\n'), 0600); err != nil {
			return err
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if r.Successes == 0 {
		return errors.New("no successful operations")
	}
	if r.ErrorRate > o.maxErrorRate {
		return fmt.Errorf("error rate %.3f exceeds %.3f", r.ErrorRate, o.maxErrorRate)
	}
	if o.maxP95 > 0 && r.P95MS > float64(o.maxP95)/float64(time.Millisecond) {
		return fmt.Errorf("p95 exceeds %s", o.maxP95)
	}
	return nil
}

func poll(ctx context.Context, client *http.Client, target string, c *collector) {
	started := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		c.add(time.Since(started), err, 0)
		return
	}
	req.Header.Set("Accept", "text/html")
	resp, err := client.Do(req)
	if err != nil {
		c.add(time.Since(started), err, 0)
		return
	}
	_, err = io.Copy(io.Discard, resp.Body)
	closeErr := resp.Body.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil && resp.StatusCode != http.StatusOK {
		err = fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	c.add(time.Since(started), err, resp.StatusCode)
}

type collector struct {
	mu        sync.Mutex
	latencies []time.Duration
	errors    int
	statuses  map[int]int
}

func (c *collector) add(d time.Duration, err error, status int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.statuses == nil {
		c.statuses = map[int]int{}
	}
	if status != 0 {
		c.statuses[status]++
	}
	if err != nil {
		c.errors++
		return
	}
	c.latencies = append(c.latencies, d)
}

type report struct {
	StartedAt      string      `json:"started_at"`
	Mode           string      `json:"mode"`
	Target         string      `json:"target"`
	Clients        int         `json:"clients"`
	IntervalMS     float64     `json:"interval_ms"`
	ElapsedSeconds float64     `json:"elapsed_seconds"`
	Requests       int         `json:"requests"`
	Successes      int         `json:"successes"`
	Errors         int         `json:"errors"`
	ErrorRate      float64     `json:"error_rate"`
	Throughput     float64     `json:"throughput_per_second"`
	P50MS          float64     `json:"p50_ms"`
	P95MS          float64     `json:"p95_ms"`
	P99MS          float64     `json:"p99_ms"`
	MaxMS          float64     `json:"max_ms"`
	Statuses       map[int]int `json:"statuses,omitempty"`
}

func (c *collector) report(elapsed time.Duration) report {
	c.mu.Lock()
	defer c.mu.Unlock()
	sort.Slice(c.latencies, func(i, j int) bool { return c.latencies[i] < c.latencies[j] })
	r := report{ElapsedSeconds: elapsed.Seconds(), Requests: len(c.latencies) + c.errors, Successes: len(c.latencies), Errors: c.errors, Statuses: c.statuses}
	if r.Requests > 0 {
		r.ErrorRate = float64(r.Errors) / float64(r.Requests)
	}
	if elapsed > 0 {
		r.Throughput = float64(r.Requests) / elapsed.Seconds()
	}
	r.P50MS = float64(percentile(c.latencies, .50)) / float64(time.Millisecond)
	r.P95MS = float64(percentile(c.latencies, .95)) / float64(time.Millisecond)
	r.P99MS = float64(percentile(c.latencies, .99)) / float64(time.Millisecond)
	r.MaxMS = float64(percentile(c.latencies, 1)) / float64(time.Millisecond)
	return r
}

// Nearest-rank percentile: ceil(p*n)-1, with no invented interpolated samples.
func percentile(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	i := int(math.Ceil(p*float64(len(sorted)))) - 1
	if i < 0 {
		i = 0
	}
	if i >= len(sorted) {
		i = len(sorted) - 1
	}
	return sorted[i]
}
