# FT-FUZZ

A fast, concurrent web directory/content discovery tool written in Go. That return the results based on a point system, giving the user a more readable print.

The source code lives in the [`FT-fuzz/`](FT-fuzz/) directory.

## How results are scored

Each probed endpoint gets a score:

```
code_point    : 200 -> 1.0, 302 -> 0.5, 403 -> 0.2, everything else -> 0
keyword_point : 1.5 if any keyword appears in the body, else 1.0
body_point    : body_size / 100000

total = code_point * keyword_point * body_point
```

## Features

- **Scoring system** — every result is ranked by HTTP status, keyword hits in the response body, and body size, so the most interesting endpoints surface first.

- **Concurrent scanning** — configurable worker pool (`-t`), shared work queue, single in-place progress bar.
- **Recursive discovery** — endpoints answering `200` or `403` become new roots; children are probed down to `-depth` levels (`-r`).
- **Flexible requests** — custom HTTP method (`-m`), custom headers (`-h`), and a request body (`-b`).
- **Scan plan & ETA** — before scanning, it measures average RTT with warm-up requests and prints an estimated request count and wall-clock time.
- **Color-coded results** — side-by-side results table grouped by status, with a "Highlighted" column for top-scoring endpoints.
- **Stealth-ish defaults** — sends browser-like `User-Agent`, `Accept`, and `Accept-Language` headers.

## Installation

### 1. Install Go

ft-fuzz requires Go 1.26 or newer.

**Debian/Ubuntu via apt** — quickest, but the packaged version may be older than 1.26:

```bash
sudo apt update && sudo apt install -y golang-go
```

**Official tarball (recommended, works on any Linux distro):**

```bash
# Check https://go.dev/dl/ for the latest version
curl -LO https://go.dev/dl/go1.26.3.linux-amd64.tar.gz
sudo rm -rf /usr/local/go
sudo tar -C /usr/local -xzf go1.26.3.linux-amd64.tar.gz
echo 'export PATH=$PATH:/usr/local/go/bin' >> ~/.bashrc
source ~/.bashrc
```

### 2. Install ft-fuzz

Clone the repo and build the binary:

```bash
git clone https://github.com/felipecoding4702/FT_fuzz.git
cd FT_fuzz/FT-fuzz
go build -o ftfuzz main.go
```

Optionally, move the binary onto your `PATH` so you can call `ftfuzz` from anywhere:

```bash
sudo mv ftfuzz /usr/local/bin/
```

## Usage

```bash
ftfuzz -u <url> -l <wordlist> [-r] [-depth N] [-t N] [-m METHOD] [-h HEADERS] [-b BODY]
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

## Example

Quick start — scan a target with the bundled wordlist:

```bash
cd FT_fuzz/FT-fuzz
./ftfuzz -u http://example.com -l wordlist.txt
```

ft-fuzz prints a scan plan first (request estimate + ETA from the measured average RTT), runs the scan with a live progress bar, then renders the results table:

```text
 SCAN PLAN
 ──────────────────────────────────────────────────────
  Scan Wordlist            wordlist.txt
  Max requests             63
  Method                   GET
  Mode                     Single-level
  Expected time            ~3.2 s   (avg RTT: 305 ms)
 ──────────────────────────────────────────────────────

[████████████████████████████████████████] 100.0% (63/63)
Done. Total requests: 63

RESULTS

Highlighted:                  200 OK:                   403 Forbidden:             404 Not Found:
────────────────────────────────────────────────────────────────────────────────────────────────────
http://…/admin(0.0150)        http://…/login(0.0031)    http://…/config(0.0002)    http://…/about(0.0000)
http://…/api(0.0122)          http://…/index(0.0028)                               http://…/contact(0.0000)
```

The most interesting endpoints (green `200`s, keyword hits in the body, larger responses) rise to the top of the **Highlighted** column with their scores. Endpoint names above are illustrative.

### More examples

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

A starter list ships in [`FT-fuzz/wordlist.txt`](FT-fuzz/wordlist.txt).

Watched keywords: `admin`, `swagger`, `robots`, `login`, `config`, `backup`, `dashboard`, `api`, `.git`, `password`.

## Output

1. **SCAN PLAN** — wordlist, method, headers/body, mode (single-level/recursive + depth), max request estimate, and expected wall-clock time based on measured average RTT.
2. **Progress bar** — one in-place bar showing percentage and completed/total requests.
3. **RESULTS** — five columns side by side: `Highlighted` (top 15 by score), `200 OK`, `302 Found`, `403 Forbidden`, `404 Not Found`. Each entry is `url(points)`, truncated to 40 rows per column with an overflow indicator.

## How it works

- The scan is a single queue-based pass: workers pop URLs, fire requests, and enqueue the children of any URL that answers `200`/`403`, descending until `maxDepth`. Work is discovered on the fly; termination is "queue empty **and** no request in flight."
- Shared state (queue, visited set, results) is guarded by a mutex; a `sync.Cond` wakes idle workers when new work appears. The completed-request counter is atomic so the progress goroutine never contends the lock.
- All network I/O happens outside the lock; a single goroutine redraws the progress bar so stdout never interleaves.

## Notes

- Before scanning, 3 warm-up GET requests are fired at the target to measure RTT. They aren't included in the request estimates and don't use your custom method/headers/body.
- Max requests with `-r` is a worst case, so the progress bar may finish below 100% when servers answer 404 early.
- Header values can't contain commas — the `-h` list splits on `,`.
- `-h` is taken by custom headers; use `-help` to list all flags.
- Requests that fail at the network level (e.g. host unreachable) are silently dropped from the results table.
