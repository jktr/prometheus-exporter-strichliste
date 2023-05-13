# prometheus-exporter-strichliste

This is a [prometheus exporter](https://prometheus.io/docs/instrumenting/exporters/)
for [strichliste v1](https://github.com/hackerspace-bootstrap/strichliste).

I wrote this mainly because I wanted alerting for a really old
strichliste v1 instance and thought it'd be a good opportunity to
familiarize myself with the the prometheus tooling.

You should probably not run this in production.

```
# scrape all users and system metrics
go run ./main.go \
  -api https://strichliste.example.com/api \
  -interval 5m \
  -bind localhost:8080

# scrape only specific users and system metrics
go run ./main.go \
  -api https://strichliste.example.com/api \
  -interval 5m \
  -bind localhost:8080 \
  1 2 3
```
