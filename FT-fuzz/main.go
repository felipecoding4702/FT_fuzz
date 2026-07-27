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
	"time"
)

type Result struct {
	url    string
	status int
	size   int
	points float64
}

type Progress struct {
	done     int
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
		pct = float64(p.done) / float64(p.maxReqs)
	}
	if pct > 1 {
		pct = 1
	}
	filled := int(pct * float64(barWidth))
	bar := strings.Repeat("█", filled) + strings.Repeat("░", barWidth-filled)
	// \033[K clears any trailing leftovers so shorter lines don't smudge.
	fmt.Printf("\r[%s] %5.1f%% (%d/%d) \033[K", bar, pct*100, p.done, p.maxReqs)
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

func newRequest(url string) (*http.Request, error) {
	/*
		Builds a GET request with browser-like headers so the traffic looks
		like a real client instead of Go's default bot UA. Shared by probe()
		and the RTT calibration so they behave identically.
	*/
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/149.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	return req, nil
}

func probe(client *http.Client, url string) (int, string) {
	/*
		Probing the connection, and retriving both the Status Code and the response body.
	*/
	req, err := newRequest(url)
	if err != nil {
		return 0, ""
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, ""
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp.StatusCode, string(body)
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
		req, err := newRequest(url)
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

func printPlan(minReqs, maxReqs, threads int, rtt, expected time.Duration) {
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
	fmt.Printf("\n%s SCAN PLAN %s\n", cBold, cReset)
	fmt.Println("  ───────────────────────────────────────────")
	fmt.Printf("  %-16s %d\n", "Min requests", minReqs)
	fmt.Printf("  %-16s %d\n", "Max requests", maxReqs)
	fmt.Printf("  %-16s %d\n", "Threads", threads)
	fmt.Printf("  %-16s %s   (avg RTT %s)\n", "Expected time", expStr, rttStr)
	fmt.Println("  ───────────────────────────────────────────")
	fmt.Println()
}

func scan(client *http.Client, base string, words []string, p *Progress) {
	/*
		Unified breadth-first scan. A work queue holds URLs to probe; each
		probed URL that answers 200 or 403 enqueues its own children so the
		search descends until maxDepth. Results are collected silently and
		rendered into tables once the scan finishes.

		The queue shape is what a future worker pool will drain, so wiring in
		threads later only changes how items are popped — not this structure.
	*/
	type item struct {
		url   string
		depth int
	}
	visited := map[string]bool{base: true}
	queue := []item{{base, 0}}

	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if cur.depth >= p.maxDepth {
			continue
		}
		for _, word := range words {
			url := cur.url + "/" + word
			if visited[url] {
				continue
			}
			visited[url] = true
			status, body := probe(client, url)
			pts := score(status, body)
			p.results = append(p.results, Result{url, status, len(body), pts})
			p.done++
			p.drawBar()
			if status == 200 || status == 403 {
				queue = append(queue, item{url, cur.depth + 1})
			}
		}
	}
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

func printTable(title, color string, rows []Result) {
	/*
		Renders one result table: a coloured header followed by every row,
		showing the URL, status code, body size and the computed points.
	*/
	fmt.Printf("\n%s%s%s (%d)%s\n", cBold, color, title, len(rows), cReset)
	fmt.Printf("  %-60s  %-5s  %-10s  %s\n", "URL", "CODE", "SIZE", "POINTS")
	if len(rows) == 0 {
		fmt.Println("  (none)")
		return
	}
	for _, r := range rows {
		fmt.Printf("  %s%-60s  %-5d  %-10d  %.4f%s\n", color, r.url, r.status, r.size, r.points, cReset)
	}
}

func (p *Progress) renderTables() {
	/*
		Sorts every probed URL by points (desc). The 15 highest go to the
		"Highlighted" table; the remainder are bucketed by status code into
		the 200 / 302 / 403 / 404 tables.
	*/
	sort.SliceStable(p.results, func(i, j int) bool {
		return p.results[i].points > p.results[j].points
	})

	// Highlight at most 15 URLs, and only ones that actually scored points.
	// Everything else (including zero-point hits) falls through to its
	// status-code table.
	var highlighted, rest []Result
	for _, r := range p.results {
		if r.points > 0 && len(highlighted) < 15 {
			highlighted = append(highlighted, r)
		} else {
			rest = append(rest, r)
		}
	}

	var t200, t302, t403, t404 []Result
	for _, r := range rest {
		switch r.status {
		case 200:
			t200 = append(t200, r)
		case 302:
			t302 = append(t302, r)
		case 403:
			t403 = append(t403, r)
		case 404:
			t404 = append(t404, r)
		}
	}

	printTable("HIGHLIGHTED — top 15 by points", cYellow, highlighted)
	printTable("200 OK", cGreen, t200)
	printTable("302 Redirect", cYellow, t302)
	printTable("403 Forbidden", cRed, t403)
	printTable("404 Not Found", cGray, t404)
}

func main() {
	wordlist := flag.String("l", "", "Path to wordlist file")
	target := flag.String("u", "", "Target URL (e.g. http://example.com)")
	depth := flag.Int("depth", 3, "Max recursion depth")
	flag.Parse()

	if *wordlist == "" || *target == "" {
		fmt.Println("Usage: ft-fuzz -u <url> -l <wordlist> [-depth N]")
		os.Exit(1)
	}

	file := openWordlist(*wordlist)
	words := loadWords(file)
	file.Close()

	minReqs := len(words)
	maxReqs, level := 0, len(words)
	for i := 0; i < *depth; i++ {
		maxReqs += level
		level *= len(words)
	}

	base := strings.TrimRight(*target, "/")
	client := &http.Client{}

	rtt := measureRTT(client, base)
	threads := 1 // parallel requests land in a later step; the estimate divides by this already.
	expectedReqs := (maxReqs + minReqs) / 2
	expectedTime := time.Duration(float64(expectedReqs) * float64(rtt) / float64(threads))

	printPlan(minReqs, maxReqs, threads, rtt, expectedTime)

	p := &Progress{maxReqs: maxReqs, maxDepth: *depth}
	scan(client, base, words, p)

	fmt.Printf("\r\033[KDone. Total requests: %d\n\n", p.done)
	p.renderTables()
}
