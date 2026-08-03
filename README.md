# 🪣 Buckets v2

A tiny in-memory pastebin with curl support, successor to
[beleon/buckets](https://github.com/beleon/buckets). One Go file, stdlib only,
one embedded HTML page, zero configuration.

Share text or files from any machine:

```bash
$ curl --data "Hello, World" http://localhost:8080/hello.txt
http://localhost:8080/ky8tex25rD8/hello.txt
```

## API

| Action   | Request                                | Notes                                        |
|----------|----------------------------------------|----------------------------------------------|
| Upload   | `POST /<filename>` with body           | Returns the bucket URL (`/<slug>/<filename>`) |
| Download | `GET /<slug>/<filename>`               | Correct `Content-Type`, ranges, `HEAD`       |
| Replace  | `POST /<slug>/<filename>` with body    | Full path must match exactly; resets the TTL |
| Delete   | `DELETE /<slug>/<filename>`            |                                              |

`GET /<slug>` redirects to the bucket's full path. `GET /` serves the web UI.

Filenames must be POSIX portable: 1–255 chars of `A-Z a-z 0-9 . _ -`, not
`.` or `..`, no leading `-`.

## Opinions (all hardcoded)

- **Port 8080**, plain HTTP. Put a reverse proxy in front for TLS.
- **24h TTL.** Replacing a bucket resets its clock.
- **500 MB max upload**, rejected while streaming with 413.
- **500 MB total storage.** When full, the oldest buckets are evicted.
- **Rate limits:** 6 POSTs/min, 60 requests/min for everything else (global,
  returns 429).
- **Slugs:** 48 bits from `crypto/rand`, 8 base64url chars. Guessing a
  bucket needs its filename too, and at the maximum rate the rate limit
  allows, the chance of hitting one bucket within its lifetime is about
  1 in 1.5 billion.
- **All in memory.** No disk, no database, nothing survives a restart.
- **Zero config.** URLs are derived from the request's `Host` header (and
  `X-Forwarded-Proto` if set). There are no environment variables.

## Build & run

```bash
go build && ./buckets-v2
```

### Docker

```bash
docker build -t buckets .
docker run --rm -p 8080:8080 --memory 1.5g buckets
```

The image is `FROM scratch` with a single static binary (the web UI is
embedded). The server shuts down gracefully on SIGTERM. Memory budget:
500 MB store + in-flight upload bodies, so ~1.5 GB is a safe container limit.

## Test

```bash
go test ./...
```
