package main

import (
	"context"
	"crypto/tls"
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/HdrHistogram/hdrhistogram-go"
	"golang.org/x/time/rate"
)

// Stats collects request metrics using an HdrHistogram.
type Stats struct {
	hist     *hdrhistogram.Histogram
	mu       sync.Mutex
	requests int64
	errors   int64
}

// Record logs a successful request latency.
func (s *Stats) Record(latency time.Duration) {
	atomic.AddInt64(&s.requests, 1)
	us := latency.Microseconds()
	s.mu.Lock()
	s.hist.RecordValue(us)
	s.mu.Unlock()
}

// RecordError logs a failed request.
func (s *Stats) RecordError() {
	atomic.AddInt64(&s.errors, 1)
}

// FinalMetrics holds summary metrics for export.
type FinalMetrics struct {
	RequestsPerSec float64       `json:"requests_per_sec"`
	TotalRequests  int64         `json:"total_requests"`
	ErrorCount     int64         `json:"error_count"`
	LatencyMin     time.Duration `json:"latency_min"`
	LatencyP50     time.Duration `json:"latency_p50"`
	LatencyP90     time.Duration `json:"latency_p90"`
	LatencyP99     time.Duration `json:"latency_p99"`
	LatencyMax     time.Duration `json:"latency_max"`
}

// ComputeMetrics builds FinalMetrics from stats.
func (s *Stats) ComputeMetrics(totalTime time.Duration) FinalMetrics {
	total := atomic.LoadInt64(&s.requests)
	errCount := atomic.LoadInt64(&s.errors)
	min := time.Microsecond * time.Duration(s.hist.Min())
	p50 := time.Microsecond * time.Duration(s.hist.ValueAtQuantile(50.0))
	p90 := time.Microsecond * time.Duration(s.hist.ValueAtQuantile(90.0))
	p99 := time.Microsecond * time.Duration(s.hist.ValueAtQuantile(99.0))
	max := time.Microsecond * time.Duration(s.hist.Max())

	return FinalMetrics{
		RequestsPerSec: float64(total) / totalTime.Seconds(),
		TotalRequests:  total,
		ErrorCount:     errCount,
		LatencyMin:     min,
		LatencyP50:     p50,
		LatencyP90:     p90,
		LatencyP99:     p99,
		LatencyMax:     max,
	}
}

func main() {
	// CLI flags
	url := flag.String("url", "http://localhost:8080", "target URL to benchmark")
	method := flag.String("method", "GET", "HTTP method to use")
	headers := flag.String("header", "", "Custom headers (comma-separated list)")
	body := flag.String("body", "", "Request body; implies POST if non-empty")
	threads := flag.Int("threads", 10, "number of concurrent workers")
	connections := flag.Int("connections", 100, "max idle connections per worker")
	duration := flag.Duration("duration", 10*time.Second, "test duration, e.g. 10s, 1m")
	rateLimit := flag.Int("rate", 0, "max requests per second (0 = no limit)")
	insecure := flag.Bool("insecure", false, "skip TLS certificate verification")
	minLatency := flag.Int("min-latency", 1, "histogram min latency µs")
	maxLatency := flag.Int("max-latency", 10000000, "histogram max latency µs")
	sigFigs := flag.Int("sig-figs", 3, "histogram precision sigfigs")
	outputFmt := flag.String("output", "text", "output format: text, json, csv")
	flag.Parse()

	// Adjust method based on body
	m := strings.ToUpper(*method)
	if *body != "" && (m == "GET" || m == "DELETE") {
		m = "POST"
	}

	// Context and rate limiter
	ctx, cancel := context.WithTimeout(context.Background(), *duration)
	defer cancel()
	var limiter *rate.Limiter
	if *rateLimit > 0 {
		limiter = rate.NewLimiter(rate.Limit(*rateLimit), *rateLimit)
	}

	// Initialize stats
	hist := hdrhistogram.New(int64(*minLatency), int64(*maxLatency), *sigFigs)
	stats := &Stats{hist: hist}

	// Parse headers
	headList := []string{}
	if *headers != "" {
		headList = strings.Split(*headers, ",")
	}

	// Start workers
	var wg sync.WaitGroup
	for i := 0; i < *threads; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: *insecure}, MaxIdleConnsPerHost: *connections}}
			for {
				select {
				case <-ctx.Done():
					return
				default:
					if limiter != nil {
						if err := limiter.Wait(ctx); err != nil {
							return
						}
					}
					req, err := http.NewRequestWithContext(ctx, m, *url, strings.NewReader(*body))
					if err != nil {
						stats.RecordError()
						continue
					}
					for _, h := range headList {
						parts := strings.SplitN(h, ":", 2)
						if len(parts) == 2 {
							req.Header.Set(strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]))
						}
					}
					start := time.Now()
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

	// Progress reporting
	startTime := time.Now()
	prevReq, prevErr := int64(0), int64(0)
	ticker := time.NewTicker(1 * time.Second)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				total := atomic.LoadInt64(&stats.requests)
				errCount := atomic.LoadInt64(&stats.errors)
				delta := total - prevReq
				dErr := errCount - prevErr
				prevReq, prevErr = total, errCount
				p50 := time.Microsecond * time.Duration(stats.hist.ValueAtQuantile(50.0))
				p90 := time.Microsecond * time.Duration(stats.hist.ValueAtQuantile(90.0))
				p99 := time.Microsecond * time.Duration(stats.hist.ValueAtQuantile(99.0))
				elapsed := int(time.Since(startTime).Seconds())
				fmt.Printf("[%ds] RPS: %.0f, Errors/s: %d, p50: %v, p90: %v, p99: %v\n",
					elapsed, float64(delta), dErr, p50, p90, p99)
			}
		}
	}()

	// Wait for completion
	wg.Wait()
	ticker.Stop()

	// Final metrics and output
	metrics := stats.ComputeMetrics(time.Since(startTime))
	switch strings.ToLower(*outputFmt) {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(metrics)
	case "csv":
		writer := csv.NewWriter(os.Stdout)
		writer.Write([]string{"requests_per_sec", "total_requests", "error_count", "latency_min", "latency_p50", "latency_p90", "latency_p99", "latency_max"})
		writer.Write([]string{
			fmt.Sprintf("%.2f", metrics.RequestsPerSec),
			fmt.Sprintf("%d", metrics.TotalRequests),
			fmt.Sprintf("%d", metrics.ErrorCount),
			metrics.LatencyMin.String(),
			metrics.LatencyP50.String(),
			metrics.LatencyP90.String(),
			metrics.LatencyP99.String(),
			metrics.LatencyMax.String(),
		})
		writer.Flush()
	default:
		fmt.Printf("\n=== Final Report ===\n")
		fmt.Printf("Requests: %d, Errors: %d\n", metrics.TotalRequests, metrics.ErrorCount)
		fmt.Printf("Latency (min/50th/90th/99th/max): %v / %v / %v / %v / %v\n",
			metrics.LatencyMin, metrics.LatencyP50, metrics.LatencyP90, metrics.LatencyP99, metrics.LatencyMax)
		fmt.Printf("Requests/sec: %.2f\n", metrics.RequestsPerSec)
	}
}
