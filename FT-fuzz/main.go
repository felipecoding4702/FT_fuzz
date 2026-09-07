package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type Result struct {
	url    string
	status int
	size   int
	points float64
}

type Progress struct {
	done     atomic.Int64 // requests completed — atomic: read by the progress goroutine without the lock
	maxReqs  int
	maxDepth int
	results  []Result
}

func (p *Progress) drawBar() {
	/*
		Draws a single progress bar in place (carriage return, no new lines),
		overwriting itself each call. Kept deliberately minimal: just the bar,
		percentage and request counter.
	*/
	const barWidth = 40
	pct := 0.0
	if p.maxReqs > 0 {
		pct = float64(p.done.Load()) / float64(p.maxReqs)
	}
	if pct > 1 {
		pct = 1
	}
	filled := int(pct * float64(barWidth))
	bar := strings.Repeat("█", filled) + strings.Repeat("░", barWidth-filled)
	// \033[K clears any trailing leftovers so shorter lines don't smudge.
	fmt.Printf("\r[%s] %5.1f%% (%d/%d) \033[K", bar, pct*100, p.done.Load(), p.maxReqs)
}

func openWordlist(path string) *os.File {
	/*
		Function designed for open the File
	*/
	file, err := os.Open(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error opening wordlist: %v\n", err)
		os.Exit(1)
	}
	return file
}

func loadWords(file *os.File) []string {
	/*
		Interpreting the words and standarizing them given a pointer to the file in system.
	*/
	var words []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		word := strings.TrimSpace(scanner.Text())
		if word == "" || strings.HasPrefix(word, "#") {
			continue
		}
		words = append(words, word)
	}
	return words
}

// keywords flag response bodies that hint at sensitive or high-value endpoints.
var keywords = []string{"admin", "swagger", "robots", "login", "config", "backup", "dashboard", "api", ".git", "password"}

func score(status int, body string) float64 {
	/*
		Point system used to rank how interesting a probed endpoint is.

		    code_point    : 200 -> 1.0, 302 -> 0.5, 403 -> 0.2, 404 -> 0
		    keyword_point : 1.5 if a keyword appears in the body, else 1.0
		    body_point    : body_size / 100000
		    total_point   = code_point * keyword_point * body_point
	*/

	var codePoint float64
	switch status {
	case 200:
		codePoint = 1.0
	case 302:
		codePoint = 0.5
	case 403:
		codePoint = 0.2
	default: // 404 and anything else
		codePoint = 0.0
	}

	keywordPoint := 1.0
	lower := strings.ToLower(body)
	for _, kw := range keywords {
		if strings.Contains(lower, kw) {
			keywordPoint = 1.5
			break
		}
	}

	bodyPoint := float64(len(body)) / 100000.0

	return codePoint * keywordPoint * bodyPoint
}

func newRequest(url string, method string, headers map[string]string, body string) (*http.Request, error) {
	/*
		Builds an HTTP request with configurable method, headers, and body.
		By default uses browser-like headers so the traffic looks like a real client.
		A fresh reader is built per call: an io.Reader is consumed by the first
		request that sends it, so sharing one would leave every later request
		with an empty body.
	*/
	var bodyReader io.Reader
	if body != "" {
		bodyReader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, url, bodyReader)
	if err != nil {
		return nil, err
	}
	// Set default browser headers (can be overridden by custom headers)
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/149.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	// Apply custom headers (overrides defaults if key matches)
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	return req, nil
}

func probe(client *http.Client, url string, method string, headers map[string]string, body string) (int, string) {
	/*
		Probing the connection, and retriving both the Status Code and the response body.
	*/
	req, err := newRequest(url, method, headers, body)
	if err != nil {
		return 0, ""
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, ""
	}
	respBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp.StatusCode, string(respBody)
}

func measureRTT(client *http.Client, url string) time.Duration {
	/*
		Fires a few warm-up requests at the base URL and returns the average
		round-trip time. Used to estimate total scan time. Returns 0 if every
		sample failed (e.g. host unreachable), which the caller treats as "n/a".
	*/
	const samples = 3
	var total time.Duration
	hit := 0
	for i := 0; i < samples; i++ {
		req, err := newRequest(url, "GET", nil, "")
		if err != nil {
			continue
		}
		start := time.Now()
		resp, err := client.Do(req)
		if err != nil {
			continue
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		total += time.Since(start)
		hit++
	}
	if hit == 0 {
		return 0
	}
	return total / time.Duration(hit)
}

func formatDuration(d time.Duration) string {
	switch {
	case d < time.Second:
		return fmt.Sprintf("%d ms", d.Milliseconds())
	case d < time.Minute:
		return fmt.Sprintf("%.1f s", d.Seconds())
	case d < time.Hour:
		return fmt.Sprintf("%.1f min", d.Minutes())
	default:
		return fmt.Sprintf("%.1f h", d.Hours())
	}
}

func printPlan(minReqs, maxReqs, threads int, rtt, expected time.Duration, recursive bool, depth int, wordlist string, method string, headers map[string]string, body string) {
	/*
		The static "SCAN PLAN" table shown once before the scan starts:
		min/max request estimates, thread count and the expected wall-clock
		time derived from the measured average RTT.
	*/
	rttStr, expStr := "n/a", "n/a"
	if rtt > 0 {
		rttStr = formatDuration(rtt)
		expStr = "~" + formatDuration(expected)
	}
	fmt.Printf("\n%s%s%s SCAN PLAN %s\n", cBold, cGreen, cReset, cReset)
	fmt.Printf("  %s%s%s\n", cGreen, strings.Repeat("─", 120), cReset)
	fmt.Printf("  %-20s\t\t\t\t\t\t\t\t\t%s\n", "Scan Wordlist", wordlist)
	fmt.Printf("  %-20s\t\t\t\t\t\t\t\t\t%s\n", "Highlighted Wordlist", "(not implemented)")
	fmt.Printf("  %-20s\t\t\t\t\t\t\t\t\t%d\n", "Max requests", maxReqs)
	fmt.Printf("  %-20s\t\t\t\t\t\t\t\t\t%s\n", "Method", method)
	if len(headers) > 0 {
		fmt.Printf("  %-20s\t\t\t\t\t\t\t\t\t", "Headers")
		for k, v := range headers {
			fmt.Printf("%s: %s ", k, v)
		}
		fmt.Println()
	}
	if body != "" {
		fmt.Printf("  %-20s\t\t\t\t\t\t\t\t\t%s\n", "Body", body)
	}
	if recursive {
		fmt.Printf("  %-20s\t\t\t\t\t\t\t\t\t%s (depth: %d)\n", "Mode", "Recursive", depth)
	} else {
		fmt.Printf("  %-20s\t\t\t\t\t\t\t\t\t%s\n", "Mode", "Single-level")
	}
	fmt.Printf("  %-20s\t\t\t\t\t\t\t\t\t%s   (avg RTT: %s)\n", "Expected time", expStr, rttStr)
	fmt.Printf("  %s%s%s\n", cGreen, strings.Repeat("─", 120), cReset)
	fmt.Println()
}

func scan(client *http.Client, base string, words []string, p *Progress, threads int, method string, headers map[string]string, body string) {
	/*
		Concurrent scan. A shared work queue holds individual URLs to probe;
		`threads` workers pop URLs, fire requests, and enqueue the children of
		any URL that answers 200 or 403, descending until maxDepth. Because
		work is discovered on the fly, there is no fixed job list — termination
		relies on the condition "queue empty AND no worker mid-flight".

		Guarding the shared state:
		  - mu protects queue, visited and results.
		  - done is atomic, so the progress goroutine reads it without the lock.
		  - cond wakes idle workers whenever the queue or the in-flight count
		    changes, so no worker sleeps past new work or past shutdown.
	*/
	if threads < 1 {
		threads = 1
	}
	if p.maxDepth < 1 {
		return
	}

	type job struct {
		url   string
		depth int
	}

	var mu sync.Mutex
	cond := sync.NewCond(&mu)
	queue := []job{}
	visited := map[string]bool{}
	active := 0 // workers currently between "popped a job" and "finished recording it"

	// Seed: enqueue the target's direct children (depth 1). The base itself is
	// never probed — it's just the root we grow the tree from.
	mu.Lock()
	for _, w := range words {
		u := base + "/" + w
		if !visited[u] {
			visited[u] = true
			queue = append(queue, job{u, 1})
		}
	}
	mu.Unlock()

	worker := func() {
		for {
			mu.Lock()
			for len(queue) == 0 {
				if active == 0 {
					// Nothing pending and nothing in flight that could add more: done.
					cond.Broadcast() // release any other sleepers so they exit too
					mu.Unlock()
					return
				}
				cond.Wait()
			}
			j := queue[0]
			queue = queue[1:]
			active++
			mu.Unlock()

			// Network I/O happens outside the lock so other workers stay busy.
			status, respBody := probe(client, j.url, method, headers, body)
			pts := score(status, respBody)
			p.done.Add(1)

			mu.Lock()
			p.results = append(p.results, Result{j.url, status, len(respBody), pts})
			if (status == 200 || status == 403) && j.depth < p.maxDepth {
				for _, w := range words {
					child := j.url + "/" + w
					if !visited[child] {
						visited[child] = true
						queue = append(queue, job{child, j.depth + 1})
					}
				}
			}
			active--
			cond.Broadcast() // new work may exist, or we may have hit the end
			mu.Unlock()
		}
	}

	// One dedicated goroutine redraws the bar off the atomic counter, so workers
	// never touch stdout and there's no interleaving/tearing.
	stop := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		p.drawBar()
		t := time.NewTicker(100 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-stop:
				p.drawBar()
				return
			case <-t.C:
				p.drawBar()
			}
		}
	}()

	var wg sync.WaitGroup
	for i := 0; i < threads; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			worker()
		}()
	}
	wg.Wait()

	close(stop)
	<-finished
}

// ANSI colors used to make the result tables readable at a glance.
const (
	cReset  = "\033[0m"
	cBold   = "\033[1m"
	cGreen  = "\033[32m"
	cYellow = "\033[33m"
	cRed    = "\033[31m"
	cGray   = "\033[90m"
)

func statusLabel(status int) string {
	/*
		Returns a human-readable label for HTTP status codes.
	*/
	switch status {
	case 200:
		return "200 OK"
	case 301:
		return "301 Moved Permanently"
	case 302:
		return "302 Found"
	case 303:
		return "303 See Other"
	case 307:
		return "307 Temporary Redirect"
	case 308:
		return "308 Permanent Redirect"
	case 403:
		return "403 Forbidden"
	case 404:
		return "404 Not Found"
	default:
		return fmt.Sprintf("%d", status)
	}
}

func statusColor(status int) string {
	/*
		Returns the color code for a given HTTP status.
	*/
	switch status {
	case 200:
		return cGreen
	case 301, 302, 303, 307, 308:
		return cYellow
	case 403:
		return cRed
	case 404:
		return cGray
	default:
		return cReset
	}
}

func (p *Progress) renderTables() {
	/*
		Renders results as a side-by-side table with fixed columns:
		Highlighted, 200 OK, 302 Found, 403 Forbidden, 404 Not Found.
		Each entry shows "url(Points)" format.
		Shows up to 40 rows with overflow indicator per column.
	*/
	const maxRows = 40

	// Sort all results by points (descending)
	sort.SliceStable(p.results, func(i, j int) bool {
		return p.results[i].points > p.results[j].points
	})

	// Highlighted: top 15 with points > 0
	var highlighted []Result
	for _, r := range p.results {
		if r.points > 0 && len(highlighted) < 15 {
			highlighted = append(highlighted, r)
		}
	}

	// Group the rest by status code
	statusGroups := make(map[int][]Result)
	seen := make(map[string]bool) // track which URLs are in highlighted
	for _, r := range highlighted {
		seen[r.url] = true
	}
	for _, r := range p.results {
		if !seen[r.url] {
			statusGroups[r.status] = append(statusGroups[r.status], r)
		}
	}

	// Build fixed columns: Highlighted, 200 OK, 302 Found, 403 Forbidden, 404 Not Found
	type column struct {
		title   string
		color   string
		results []Result
	}
	var columns []column

	// Always add these fixed columns
	columns = append(columns, column{"Highlighted", cYellow, highlighted})
	columns = append(columns, column{"200 OK", cGreen, statusGroups[200]})
	columns = append(columns, column{"302 Found", cYellow, statusGroups[302]})
	columns = append(columns, column{"403 Forbidden", cRed, statusGroups[403]})
	columns = append(columns, column{"404 Not Found", cGray, statusGroups[404]})

	// Calculate column width (based on terminal width and number of columns)
	// Default to 30 chars per column if we have many columns, wider if fewer
	colWidth := 30
	if len(columns) <= 2 {
		colWidth = 45
	} else if len(columns) <= 3 {
		colWidth = 35
	}

	fmt.Printf("\n%sRESULTS%s\n\n", cBold, cReset)

	// Print header row
	for i, col := range columns {
		if i > 0 {
			fmt.Printf("  ")
		}
		header := col.title + ":"
		fmt.Printf("%s%-*s%s", col.color, colWidth, header, cReset)
	}
	fmt.Println()

	// Print separator line
	for i := range columns {
		if i > 0 {
			fmt.Printf("  ")
		}
		fmt.Printf("%s", strings.Repeat("─", colWidth))
	}
	fmt.Println()

	// Print data rows
	for row := 0; row < maxRows+1; row++ {
		for i, col := range columns {
			if i > 0 {
				fmt.Printf("  ")
			}
			color := col.color

			// Get entry for this row
			var url string
			var points string
			displayCount := min(len(col.results), maxRows)
			if row < displayCount {
				r := col.results[row]
				url = r.url
				points = fmt.Sprintf("(%.4f)", r.points)
			} else if row == displayCount && len(col.results) > maxRows {
				// Show overflow indicator at the end
				url = ""
				points = fmt.Sprintf("(+%d more)", len(col.results)-maxRows)
			} else {
				url = ""
				points = ""
			}

			// Print entry with URL in default color and points in column color
			// Exception: for 404, color the entire entry
			if url == "" && points == "" {
				fmt.Printf("%-*s", colWidth, "")
			} else if col.title == "404 Not Found" {
				// For 404, color the entire entry
				entry := url + points
				fmt.Printf("%s%s%s", color, entry, cReset)
				remaining := colWidth - len(entry)
				if remaining > 0 {
					fmt.Printf("%*s", remaining, "")
				}
			} else {
				// For other columns, color only the points
				fmt.Printf("%s%s%s%s", url, color, points, cReset)
				remaining := colWidth - len(url) - len(points)
				if remaining > 0 {
					fmt.Printf("%*s", remaining, "")
				}
			}
		}
		fmt.Println()

		// Check if any column has more data
		allDone := true
		for _, col := range columns {
			displayCount := min(len(col.results), maxRows)
			hasOverflow := len(col.results) > maxRows
			maxRowToPrint := displayCount
			if hasOverflow {
				maxRowToPrint++ // need one more row for overflow indicator
			}
			if row+1 < maxRowToPrint {
				allDone = false
				break
			}
		}
		if allDone {
			break
		}
	}

	fmt.Println()
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func parseHeaders(headerStr string) map[string]string {
	/*
		Parses headers from the format "key1:value1,key2:value2"
		Returns a map of header key-value pairs.
	*/
	headers := make(map[string]string)
	if headerStr == "" {
		return headers
	}
	pairs := strings.Split(headerStr, ",")
	for _, pair := range pairs {
		kv := strings.SplitN(pair, ":", 2)
		if len(kv) == 2 {
			headers[strings.TrimSpace(kv[0])] = strings.TrimSpace(kv[1])
		}
	}
	return headers
}

func main() {
	wordlist := flag.String("l", "", "Path to wordlist file")
	target := flag.String("u", "", "Target URL (e.g. http://example.com)")
	recursive := flag.Bool("r", false, "Enable recursive scanning")
	depth := flag.Int("depth", 3, "Max recursion depth (requires -r)")
	threads := flag.Int("t", 10, "Number of concurrent workers")
	method := flag.String("m", "GET", "HTTP method to use (GET, POST, PUT, DELETE, etc.)")
	headers := flag.String("h", "", "Custom headers in format 'key1:value1,key2:value2'")
	body := flag.String("b", "", "Request body to send")
	flag.Parse()

	if *wordlist == "" || *target == "" {
		fmt.Println("Usage: ft-fuzz -u <url> -l <wordlist> [-r] [-depth N] [-t N] [-m METHOD] [-h HEADERS] [-b BODY]")
		fmt.Println("  -u URL      : Target URL (e.g. http://example.com)")
		fmt.Println("  -l PATH     : Path to wordlist file")
		fmt.Println("  -r          : Enable recursive scanning")
		fmt.Println("  -depth N    : Max recursion depth (default: 3, requires -r)")
		fmt.Println("  -t N        : Number of concurrent workers (default: 10)")
		fmt.Println("  -m METHOD   : HTTP method to use (default: GET)")
		fmt.Println("  -h HEADERS  : Custom headers in format 'key1:value1,key2:value2'")
		fmt.Println("  -b BODY     : Request body to send")
		os.Exit(1)
	}
	if *threads < 1 {
		*threads = 1
	}
	if *method == "" {
		*method = "GET"
	}

	// When not recursive, depth is effectively 1 (single-level scan only)
	effectiveDepth := *depth
	if !*recursive {
		effectiveDepth = 1
	}

	file := openWordlist(*wordlist)
	words := loadWords(file)
	file.Close()

	// Parse headers
	parsedHeaders := parseHeaders(*headers)

	// Calculate request estimates based on recursive/non-recursive mode
	minReqs := len(words)
	maxReqs := minReqs
	if *recursive {
		// Worst case: every probed URL answers 200/403 and spawns a full
		// level of children — N + N² + … + N^depth requests.
		level := len(words)
		maxReqs = 0
		for i := 0; i < effectiveDepth; i++ {
			maxReqs += level
			level *= len(words)
		}
	}

	base := strings.TrimRight(*target, "/")
	client := &http.Client{}

	rtt := measureRTT(client, base)
	expectedReqs := (maxReqs + minReqs) / 2
	expectedTime := time.Duration(float64(expectedReqs) * float64(rtt) / float64(*threads))

	printPlan(minReqs, maxReqs, *threads, rtt, expectedTime, *recursive, effectiveDepth, *wordlist, *method, parsedHeaders, *body)

	p := &Progress{maxReqs: maxReqs, maxDepth: effectiveDepth}
	scan(client, base, words, p, *threads, *method, parsedHeaders, *body)

	fmt.Printf("\r\033[KDone. Total requests: %d\n\n", p.done.Load())
	p.renderTables()
}
