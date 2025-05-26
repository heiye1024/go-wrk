# go-wrk: A simple wrk-like benchmark tool in Go
 
Usage:
go build -o go-wrk main.go
./go-wrk -u https:example.com -t 50 -c 200 -d 30s         # HTTP over TLS
./go-wrk -u https:example.com -t 100 -c 500 -d 1m -insecure  # skip cert verification
./go-wrk -u http:localhost:8080 -t 10 -c 100 -d 10s -min 1 -max 10000000 -sigfigs 3
Flags:
-u         target URL (supports http/https)
-t         number of concurrent workers (default 10)
-c         max idle connections per worker (default 100)
-d         test duration, e.g. 10s, 1m (default 10s)
-insecure  skip TLS certificate verification (default false)
-min       histogram min latency µs (default 1)
-max       histogram max latency µs (default 10000000)
-sigfigs   histogram precision significant figures (default 3)
