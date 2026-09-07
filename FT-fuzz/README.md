# ft-fuzz

A fast, concurrent web directory/content discovery tool written in Go. ft-fuzz probes paths from a wordlist against a target URL, scores how interesting each endpoint is, and can recurse into directories that respond, fanning out a tree of probes up to a configurable depth.

> ⚠️ Use only against systems you are authorized to test.

## Features

- **Concurrent scanning** — configurable worker pool (`-t`), shared work queue, single in-place progress bar.
- **Recursive discovery** — endpoints answering `200` or `403` become new roots; children are probed down to `-depth` levels (`-r`).
- **Scoring system** — every result is ranked by HTTP status, keyword hits in the response body, and body size, so the most interesting endpoints surface first.
- **Flexible requests** — custom HTTP method (`-m`), custom headers (`-h`), and a request body (`-b`).
- **Scan plan & ETA** — before scanning, it measures average RTT with warm-up requests and prints an estimated request count and wall-clock time.
- **Color-coded results** — side-by-side results table grouped by status, with a "Highlighted" column for top-scoring endpoints.
- **Stealth-ish defaults** — sends browser-like `User-Agent`, `Accept`, and `Accept-Language` headers.

## Build

Requires Go 1.26+.

```bash
go build -o ftfuzz main.go
```

## Usage

```bash
ft-fuzz -u <url> -l <wordlist> [-r] [-depth N] [-t N] [-m METHOD] [-h HEADERS] [-b BODY]
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `-u` | — | Target URL (e.g. `http://example.com`) — **required** |
| `-l` | — | Path to wordlist file — **required** |
| `-r` | off | Enable recursive scanning |
| `-depth` | `3` | Max recursion depth (requires `-r`) |
| `-t` | `10` | Number of concurrent workers |
| `-m` | `GET` | HTTP method (`GET`, `POST`, `PUT`, `DELETE`, …) |
| `-h` | — | Custom headers, format `'key1:value1,key2:value2'` |
| `-b` | — | Request body to send |

### Examples

Single-level scan:

```bash
./ftfuzz -u http://example.com -l wordlist.txt
```

Recursive scan, 3 levels deep, 20 workers:

```bash
./ftfuzz -u http://example.com -l wordlist.txt -r -depth 3 -t 20
```

POST with JSON body and an auth header:

```bash
./ftfuzz -u http://example.com -l wordlist.txt -m POST \
  -h "Content-Type:application/json,Authorization:Bearer TOKEN" \
  -b '{"query":"1"}'
```

## Wordlist format

Plain text, one entry per line. Blank lines and lines starting with `#` are ignored. Entries are appended to the target URL (`http://example.com/admin`) and, when recursing, to each discovered directory.

A starter list ships in [`wordlist.txt`](wordlist.txt).

## How results are scored

Each probed endpoint gets a score:

```
code_point    : 200 -> 1.0, 302 -> 0.5, 403 -> 0.2, everything else -> 0
keyword_point : 1.5 if any keyword appears in the body, else 1.0
body_point    : body_size / 100000

total = code_point * keyword_point * body_point
```

Watched keywords: `admin`, `swagger`, `robots`, `login`, `config`, `backup`, `dashboard`, `api`, `.git`, `password`.

## Output

1. **SCAN PLAN** — wordlist, method, headers/body, mode (single-level/recursive + depth), max request estimate, and expected wall-clock time based on measured average RTT.
2. **Progress bar** — one in-place bar showing percentage and completed/total requests.
3. **RESULTS** — five columns side by side: `Highlighted` (top 15 by score), `200 OK`, `302 Found`, `403 Forbidden`, `404 Not Found`. Each entry is `url(points)`, truncated to 40 rows per column with an overflow indicator.

## How it works

- The scan is a single queue-based pass: workers pop URLs, fire requests, and enqueue the children of any URL that answers `200`/`403`, descending until `maxDepth`. Work is discovered on the fly; termination is "queue empty **and** no request in flight."
- Shared state (queue, visited set, results) is guarded by a mutex; a `sync.Cond` wakes idle workers when new work appears. The completed-request counter is atomic so the progress goroutine never contends the lock.
- All network I/O happens outside the lock; a single goroutine redraws the progress bar so stdout never interleaves.

## Notes & limitations

- The base URL itself is never probed — it's only the root the tree grows from.
- Max requests with `-r` grows exponentially: `N + N² + … + N^depth` for a wordlist of `N` words. Budget accordingly.
- The "Highlighted Wordlist" option shown in the scan plan is not implemented yet.
- Results are in-memory only; there is no output-file/report export yet.
