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
	depth    int
	maxDepth int
	drawn    bool
	results  []Result
}

func (p *Progress) print(url string, status, size int, pts float64, isResult bool) {
	/*
		Function related to the printing stage of the tool.
		Both controlling the advance of the percetage Bar such as the current node printing
	*/

	if p.drawn {
		fmt.Print("\033[1A\033[2K\r\033[1A\033[2K\r")
		p.drawn = false
	}
	if isResult {
		if status == 0 {
			fmt.Printf("%-60s  ERROR\n", url)
		} else {
			fmt.Printf("%-60s  %d  (%d bytes)  [%.3f pts]\n", url, status, size, pts)
		}
		p.done++
	}
	if url != "" {
		const barWidth = 40
		pct := 0.0
		if p.maxReqs > 0 {
			pct = float64(p.done) / float64(p.maxReqs)
		}
		filled := int(pct * float64(barWidth))
		bar := strings.Repeat("█", filled) + strings.Repeat("░", barWidth-filled)
		fmt.Printf("Testing : %-70s\n[%s] %5.1f%% (%d/%d) | depth %d/%d\n",
			url, bar, pct*100, p.done, p.maxReqs, p.depth, p.maxDepth)
		p.drawn = true
	}
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

func probe(client *http.Client, url string) (int, string) {
	/*
		Setting up the HTTP Client given its pointer, so that we can set up Headers that disguises the nature of a bot.
		Probing the connection, and retriving both the Status Code and the response body.
	*/

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return 0, ""
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/149.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")

	resp, err := client.Do(req)
	if err != nil {
		return 0, ""
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp.StatusCode, string(body)
}

func dfs_scan(client *http.Client, base string, words []string, visited map[string]bool, p *Progress, depth int) {
	/*
		DFS(Depth First Search) algorithm that utilizes a recursive fucntion to probe further connections when status Code is either 200 or 403
		and having as stoping poitn both Status Code 404, 302... or The depth of the search
	*/
	if depth >= p.maxDepth {
		return
	}
	for _, word := range words {
		url := base + "/" + word
		if visited[url] {
			continue
		}
		visited[url] = true
		p.depth = depth + 1
		p.print(url, 0, 0, 0, false)
		status, body := probe(client, url)
		pts := score(status, body)
		p.results = append(p.results, Result{url, status, len(body), pts})
		p.print(url, status, len(body), pts, true)
		if status == 200 || status == 403 {
			dfs_scan(client, url, words, visited, p, depth+1)
			p.depth = depth + 1
		}
	}
}

func bfs_scan(client *http.Client, base string, words []string, p *Progress) {
	type item struct {
		url   string
		depth int
	}
	visited := map[string]bool{base: true}
	queue := []item{{base, 0}}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		if current.depth >= p.maxDepth {
			continue
		}
		for _, word := range words {
			url := current.url + "/" + word
			if visited[url] {
				continue
			}
			visited[url] = true
			p.depth = current.depth + 1
			p.print(url, 0, 0, 0, false)
			status, body := probe(client, url)
			pts := score(status, body)
			p.results = append(p.results, Result{url, status, len(body), pts})
			p.print(url, status, len(body), pts, true)
			if status == 200 || status == 403 {
				queue = append(queue, item{url, current.depth + 1})
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
	dfs := flag.Bool("dfs", false, "Depth-first scan")
	bfs := flag.Bool("bfs", false, "Breadth-first scan")
	depth := flag.Int("depth", 3, "Max recursion depth")
	flag.Parse()

	if *wordlist == "" || *target == "" || (!*dfs && !*bfs) {
		fmt.Println("Usage: ft-fuzz -u <url> -l <wordlist> [--dfs|--bfs] [-depth N]")
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
	fmt.Printf("Wordlist: %d words | depth: %d | min: %d requests | max (worst case): %d requests\n\n",
		len(words), *depth, minReqs, maxReqs)

	base := strings.TrimRight(*target, "/")
	client := &http.Client{}
	p := &Progress{maxReqs: maxReqs, maxDepth: *depth}

	if *dfs {
		dfs_scan(client, base, words, map[string]bool{}, p, 0)
	} else {
		bfs_scan(client, base, words, p)
	}

	p.print("", 0, 0, 0, false)
	fmt.Printf("\nDone. Total requests: %d\n", p.done)
	p.renderTables()
}
