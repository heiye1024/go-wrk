package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/HdrHistogram/hdrhistogram-go"
)

// Stats collects request metrics using an HdrHistogram.
type Stats struct {
	hist      *hdrhistogram.Histogram
	mu        sync.Mutex
	requests  int64
	errors    int64
}

// Record logs a successful request latency.
func (s *Stats) Record(latency time.Duration) {
	atomic.AddInt64(&s.requests, 1)
	// record in microseconds
	us := latency.Microseconds()
	s.mu.Lock()
	s.hist.RecordValue(us)
	s.mu.Unlock()
}

// RecordError logs a failed request.
func (s *Stats) RecordError() {
	atomic.AddInt64(&s.errors, 1)
}

// Report prints summary of the collected metrics.
func (s *Stats) Report(totalTime time.Duration) {
	total := atomic.LoadInt64(&s.requests)
	errCount := atomic.LoadInt64(&s.errors)

	fmt.Printf("Requests: %d, Errors: %d\n", total, errCount)
	if total > 0 {
		min := time.Microsecond * time.Duration(s.hist.Min())
		p50 := time.Microsecond * time.Duration(s.hist.ValueAtQuantile(50.0))
		p90 := time.Microsecond * time.Duration(s.hist.ValueAtQuantile(90.0))
		p99 := time.Microsecond * time.Duration(s.hist.ValueAtQuantile(99.0))
		max := time.Microsecond * time.Duration(s.hist.Max())

		fmt.Printf("Latency (min/50th/90th/99th/max): %v / %v / %v / %v / %v\n", min, p50, p90, p99, max)
		fmt.Printf("Requests/sec: %.2f\n", float64(total)/totalTime.Seconds())
	}
}

func main() {
	var (
		threads  = flag.Int("t", 10, "number of concurrent workers")
		conn     = flag.Int("c", 100, "max idle connections per worker")
		dur      = flag.Duration("d", 10*time.Second, "test duration, e.g. 10s, 1m")
		url      = flag.String("u", "http://localhost:8080", "target URL to benchmark (supports http/https)")
		insecure = flag.Bool("insecure", false, "skip TLS certificate verification")
		minUs    = flag.Int("min", 1, "minimum latency for histogram (µs)")
		maxUs    = flag.Int("max", 10000000, "maximum latency for histogram (µs)")
		sigFigs  = flag.Int("sigfigs", 3, "significant figures for histogram precision")
	)
	flag.Parse()

	// Prepare context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), *dur)
	defer cancel()

	// Initialize stats and histogram
	hist := hdrhistogram.New(int64(*minUs), int64(*maxUs), *sigFigs)
	stats := &Stats{hist: hist}
	var wg sync.WaitGroup

	// Launch workers
	for i := 0; i < *threads; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			client := &http.Client{
				Transport: &http.Transport{
					TLSClientConfig:     &tls.Config{InsecureSkipVerify: *insecure},
					MaxIdleConnsPerHost: *conn,
				},
			}

			for {
				select {
				case <-ctx.Done():
					return
				default:
					start := time.Now()
					req, _ := http.NewRequestWithContext(ctx, "GET", *url, nil)
					resp, err := client.Do(req)
					lat := time.Since(start)
					if err != nil {
						stats.RecordError()
						continue
					}
					io.Copy(io.Discard, resp.Body)
					resp.Body.Close()
					stats.Record(lat)
				}
			}
		}()
	}

	wg.Wait()

	// Output report
	stats.Report(*dur)
}
