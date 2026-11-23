// Command federation is KilatMart's query-federation service (IF4052 option A2).
//
// It embeds DuckDB, ATTACHes the web shop's PostgreSQL orders database and the
// inventory's MySQL stock database (read-only), and answers the analytics team's
// questions with one SQL query across both. Nothing is copied or stored: every
// answer is read from the source databases at query time.
package main

import (
	"context"
	"database/sql"
	"embed"
	"encoding/csv"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	_ "github.com/duckdb/duckdb-go/v2"
)

//go:embed queries/*.sql
var queryFS embed.FS

// Source is one database that DuckDB attaches.
type Source struct {
	Name  string // friendly name used in API responses
	Alias string // DuckDB catalog name used in SQL
	Type  string // DuckDB extension: postgres | mysql
	DSN   string // libpq-style key=value connection string
}

type config struct {
	addr         string
	mode         string // fresh | persistent
	timeout      time.Duration
	extensionDir string
	maxRows      int
	sources      []Source
}

func loadConfig() config {
	return config{
		addr:         env("LISTEN_ADDR", ":8080"),
		mode:         env("FEDERATION_MODE", "fresh"),
		timeout:      envDuration("QUERY_TIMEOUT", 10*time.Second),
		extensionDir: env("DUCKDB_EXTENSION_DIR", "/opt/duckdb/extensions"),
		maxRows:      1000,
		sources: []Source{
			{Name: "orders (PostgreSQL)", Alias: "orders_pg", Type: "postgres",
				DSN: env("ORDERS_DSN", "host=orders-db port=5432 dbname=kilatmart user=analytics_ro password=analytics_ro connect_timeout=3")},
			{Name: "stock (MySQL)", Alias: "stock_my", Type: "mysql",
				DSN: env("STOCK_DSN", "host=stock-db port=3306 database=kilatmart user=analytics_ro password=analytics_ro")},
		},
	}
}

func main() {
	installOnly := flag.Bool("install-extensions", false, "download the postgres and mysql DuckDB extensions, then exit (used at image build time)")
	healthcheck := flag.Bool("healthcheck", false, "probe the local /healthz endpoint, then exit (used by docker compose)")
	flag.Parse()
	cfg := loadConfig()

	switch {
	case *installOnly:
		if err := installExtensions(cfg.extensionDir); err != nil {
			log.Fatalf("install extensions: %v", err)
		}
		log.Printf("extensions installed in %s", cfg.extensionDir)
		return
	case *healthcheck:
		resp, err := http.Get("http://127.0.0.1" + cfg.addr + "/healthz")
		if err != nil || resp.StatusCode != http.StatusOK {
			os.Exit(1)
		}
		return
	}

	reports, err := loadReports()
	if err != nil {
		log.Fatalf("load reports: %v", err)
	}
	f := &federator{cfg: cfg}
	srv := &server{cfg: cfg, fed: f, reports: reports}

	log.Printf("federation service listening on %s (engine=duckdb, mode=%s, timeout=%s)", cfg.addr, cfg.mode, cfg.timeout)
	for _, s := range cfg.sources {
		log.Printf("  source %-20s -> %s (%s)", s.Name, s.Alias, redact(s.DSN))
	}
	log.Fatal(http.ListenAndServe(cfg.addr, srv.routes()))
}

// ---------------------------------------------------------------------------
// Federation engine (DuckDB)
// ---------------------------------------------------------------------------

// federator owns the DuckDB engine. In "fresh" mode every request gets a new
// in-memory DuckDB with freshly attached sources (no state survives between
// requests). In "persistent" mode one DuckDB instance keeps its attachments
// for the life of the process — useful to compare in the failure experiment.
type federator struct {
	cfg  config
	mu   sync.Mutex
	conn *sql.Conn // persistent mode only
	db   *sql.DB
}

// SourceError says which source failed to attach.
type SourceError struct {
	Source Source
	Err    error
}

func (e *SourceError) Error() string {
	return fmt.Sprintf("source %s unavailable: %v", e.Source.Name, e.Err)
}
func (e *SourceError) Unwrap() error { return e.Err }

func installExtensions(dir string) error {
	db, err := sql.Open("duckdb", "")
	if err != nil {
		return err
	}
	defer db.Close()
	for _, stmt := range []string{
		fmt.Sprintf("SET extension_directory = %s", quote(dir)),
		"INSTALL postgres", "INSTALL mysql",
		"LOAD postgres", "LOAD mysql",
	} {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("%s: %w", stmt, err)
		}
	}
	return nil
}

// open creates a DuckDB connection with both sources attached read-only.
func (f *federator) open(ctx context.Context) (*sql.DB, *sql.Conn, error) {
	db, err := sql.Open("duckdb", "")
	if err != nil {
		return nil, nil, err
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		db.Close()
		return nil, nil, err
	}
	setup := []string{
		fmt.Sprintf("SET extension_directory = %s", quote(f.cfg.extensionDir)),
		"SET autoinstall_known_extensions = false", // offline: never download at run time
		"LOAD postgres", "LOAD mysql",
	}
	for _, stmt := range setup {
		if _, err := conn.ExecContext(ctx, stmt); err != nil {
			conn.Close()
			db.Close()
			return nil, nil, fmt.Errorf("%s: %w", stmt, err)
		}
	}
	for _, s := range f.cfg.sources {
		stmt := fmt.Sprintf("ATTACH %s AS %s (TYPE %s, READ_ONLY)", quote(s.DSN), s.Alias, s.Type)
		if _, err := conn.ExecContext(ctx, stmt); err != nil {
			conn.Close()
			db.Close()
			return nil, nil, &SourceError{Source: s, Err: err}
		}
	}
	// Ad-hoc SQL must not be able to read the container's files or change settings.
	for _, stmt := range []string{
		"SET disabled_filesystems = 'LocalFileSystem'",
		"SET lock_configuration = true",
	} {
		if _, err := conn.ExecContext(ctx, stmt); err != nil {
			conn.Close()
			db.Close()
			return nil, nil, fmt.Errorf("%s: %w", stmt, err)
		}
	}
	return db, conn, nil
}

// Result is a query answer.
type Result struct {
	Columns []string `json:"columns"`
	Rows    [][]any  `json:"rows"`
}

// Query runs SQL across the attached sources, bounded by the configured timeout.
func (f *federator) Query(ctx context.Context, query string) (*Result, error) {
	ctx, cancel := context.WithTimeout(ctx, f.cfg.timeout)
	defer cancel()

	type outcome struct {
		res *Result
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		res, err := f.query(ctx, query)
		done <- outcome{res, err}
	}()
	// A source that hangs (for example a paused container) can block inside the
	// database driver; the select guarantees the caller still gets an answer.
	select {
	case o := <-done:
		return o.res, o.err
	case <-ctx.Done():
		return nil, fmt.Errorf("query timed out after %s: %w", f.cfg.timeout, ctx.Err())
	}
}

func (f *federator) query(ctx context.Context, query string) (*Result, error) {
	if f.cfg.mode != "persistent" {
		db, conn, err := f.open(ctx)
		if err != nil {
			return nil, err
		}
		defer db.Close()
		defer conn.Close()
		return run(ctx, conn, query, f.cfg.maxRows)
	}

	// Persistent mode: attach once, reuse forever (until the process restarts).
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.conn == nil {
		db, conn, err := f.open(ctx)
		if err != nil {
			return nil, err
		}
		f.db, f.conn = db, conn
	}
	return run(ctx, f.conn, query, f.cfg.maxRows)
}

func run(ctx context.Context, conn *sql.Conn, query string, maxRows int) (*Result, error) {
	rows, err := conn.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	res := &Result{Columns: cols, Rows: [][]any{}}
	for rows.Next() {
		if len(res.Rows) >= maxRows {
			break
		}
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		for i, v := range vals {
			vals[i] = normalize(v)
		}
		res.Rows = append(res.Rows, vals)
	}
	return res, rows.Err()
}

// Probe checks each source on its own, so an error can name the broken one.
func (f *federator) Probe(ctx context.Context) map[string]string {
	status := make(map[string]string, len(f.cfg.sources))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, s := range f.cfg.sources {
		wg.Add(1)
		go func(s Source) {
			defer wg.Done()
			one := &federator{cfg: f.cfg}
			one.cfg.mode = "fresh"
			one.cfg.sources = []Source{s}
			one.cfg.timeout = 5 * time.Second
			msg := "up"
			if _, err := one.Query(ctx, "SELECT 1"); err != nil {
				msg = "down: " + redactSecrets(firstLine(err.Error()))
			}
			mu.Lock()
			status[s.Name] = msg
			mu.Unlock()
		}(s)
	}
	wg.Wait()
	return status
}

// ---------------------------------------------------------------------------
// HTTP API for the analytics team
// ---------------------------------------------------------------------------

type report struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	SQL         string `json:"sql"`
}

func loadReports() (map[string]report, error) {
	out := map[string]report{}
	err := fs.WalkDir(queryFS, "queries", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := queryFS.ReadFile(p)
		if err != nil {
			return err
		}
		name := strings.TrimSuffix(path.Base(p), ".sql")
		out[name] = report{Name: name, Description: describe(string(b)), SQL: string(b)}
		return nil
	})
	return out, err
}

type server struct {
	cfg     config
	fed     *federator
	reports map[string]report
}

func (s *server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.index)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "ok\n") })
	mux.HandleFunc("GET /sources", s.sources)
	mux.HandleFunc("GET /reports", s.listReports)
	mux.HandleFunc("GET /reports/{name}", s.runReport)
	mux.HandleFunc("POST /sql", s.adHoc)
	return logRequests(mux)
}

type answer struct {
	Report      string    `json:"report,omitempty"`
	Engine      string    `json:"engine"`
	Mode        string    `json:"mode"`
	GeneratedAt time.Time `json:"generated_at"`
	ElapsedMS   int64     `json:"elapsed_ms"`
	RowCount    int       `json:"row_count"`
	*Result
}

type failure struct {
	Error        string            `json:"error"`
	FailedSource string            `json:"failed_source,omitempty"`
	Detail       string            `json:"detail"`
	Sources      map[string]string `json:"sources,omitempty"`
	ElapsedMS    int64             `json:"elapsed_ms"`
}

func (s *server) index(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	indexTmpl.Execute(w, map[string]any{"Reports": s.sortedReports(), "Mode": s.cfg.mode})
}

func (s *server) sources(w http.ResponseWriter, r *http.Request) {
	status := s.fed.Probe(r.Context())
	code := http.StatusOK
	for _, v := range status {
		if v != "up" {
			code = http.StatusServiceUnavailable
		}
	}
	writeJSON(w, code, status)
}

func (s *server) listReports(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.sortedReports())
}

func (s *server) runReport(w http.ResponseWriter, r *http.Request) {
	rep, ok := s.reports[r.PathValue("name")]
	if !ok {
		writeJSON(w, http.StatusNotFound, failure{Error: "unknown report", Detail: "see GET /reports"})
		return
	}
	s.answer(w, r, rep.Name, rep.SQL)
}

func (s *server) adHoc(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	if err != nil || strings.TrimSpace(string(body)) == "" {
		writeJSON(w, http.StatusBadRequest, failure{Error: "empty query", Detail: "send SQL as the request body"})
		return
	}
	s.answer(w, r, "", string(body))
}

func (s *server) answer(w http.ResponseWriter, r *http.Request, name, query string) {
	start := time.Now()
	res, err := s.fed.Query(r.Context(), query)
	elapsed := time.Since(start).Milliseconds()
	if err != nil {
		f := failure{Error: "federated query failed", Detail: redactSecrets(firstLine(err.Error())), ElapsedMS: elapsed}
		code := http.StatusBadRequest // default: problem in the SQL itself
		var se *SourceError
		switch {
		case errors.As(err, &se):
			code, f.FailedSource = http.StatusServiceUnavailable, se.Source.Name
			f.Sources = s.statusFromAttach(se.Source)
		case errors.Is(err, context.DeadlineExceeded):
			code = http.StatusGatewayTimeout
		case looksLikeSourceFailure(err):
			code = http.StatusServiceUnavailable
		}
		if code != http.StatusBadRequest && f.Sources == nil {
			f.Sources = s.fed.Probe(context.Background())
		}
		log.Printf("query failed (%d) after %dms: %s", code, elapsed, redactSecrets(err.Error()))
		writeJSON(w, code, f)
		return
	}

	a := answer{Report: name, Engine: "duckdb", Mode: s.cfg.mode, GeneratedAt: time.Now(),
		ElapsedMS: elapsed, RowCount: len(res.Rows), Result: res}
	switch format(r) {
	case "csv":
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		cw := csv.NewWriter(w)
		cw.Write(res.Columns)
		for _, row := range res.Rows {
			rec := make([]string, len(row))
			for i, v := range row {
				rec[i] = cellText(v)
			}
			cw.Write(rec)
		}
		cw.Flush()
	case "html":
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		reportTmpl.Execute(w, a)
	default:
		writeJSON(w, http.StatusOK, a)
	}
}

// statusFromAttach derives source status from the attach order: sources are
// attached one by one, so those before the failed one were reachable.
func (s *server) statusFromAttach(failed Source) map[string]string {
	status := map[string]string{}
	seen := false
	for _, src := range s.cfg.sources {
		switch {
		case src.Alias == failed.Alias:
			status[src.Name], seen = "down", true
		case !seen:
			status[src.Name] = "up"
		default:
			status[src.Name] = "not checked"
		}
	}
	return status
}

func (s *server) sortedReports() []report {
	out := make([]report, 0, len(s.reports))
	for _, r := range s.reports {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func format(r *http.Request) string {
	if f := r.URL.Query().Get("format"); f != "" {
		return f
	}
	if strings.Contains(r.Header.Get("Accept"), "text/html") {
		return "html"
	}
	return "json"
}

func normalize(v any) any {
	switch x := v.(type) {
	case []byte:
		return string(x)
	case time.Time:
		return x.Format(time.RFC3339)
	case interface{ Float64() float64 }: // DuckDB DECIMAL
		return x.Float64()
	default:
		return x
	}
}

func cellText(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}

func looksLikeSourceFailure(err error) bool {
	msg := strings.ToLower(err.Error())
	for _, hint := range []string{"connection", "connect", "server has gone away", "lost connection", "could not translate host", "unknown mysql server host", "timeout"} {
		if strings.Contains(msg, hint) {
			return true
		}
	}
	return false
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(v)
}

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		if r.URL.Path != "/healthz" {
			log.Printf("%s %s (%s)", r.Method, r.URL.RequestURI(), time.Since(start).Round(time.Millisecond))
		}
	})
}

// describe returns the leading SQL comment block of a query file.
func describe(sqlText string) string {
	var parts []string
	for _, line := range strings.Split(sqlText, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "--") {
			break
		}
		parts = append(parts, strings.TrimSpace(strings.TrimPrefix(line, "--")))
	}
	return strings.Join(parts, " ")
}

func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

func redact(dsn string) string {
	fields := strings.Fields(dsn)
	for i, f := range fields {
		if strings.HasPrefix(f, "password=") {
			fields[i] = "password=***"
		}
	}
	return strings.Join(fields, " ")
}

var passwordRe = regexp.MustCompile(`password=\S+?("|\s|$)`)

// redactSecrets hides passwords that DuckDB echoes back inside error messages.
func redactSecrets(s string) string { return passwordRe.ReplaceAllString(s, "password=***$1") }

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envDuration(key string, def time.Duration) time.Duration {
	if d, err := time.ParseDuration(os.Getenv(key)); err == nil {
		return d
	}
	return def
}

var indexTmpl = template.Must(template.New("index").Parse(`<!doctype html>
<html><head><meta charset="utf-8"><title>KilatMart federation</title>
<style>body{font-family:system-ui,sans-serif;max-width:820px;margin:2rem auto;padding:0 1rem;line-height:1.5}
code{background:#f2f2f2;padding:.1rem .3rem;border-radius:3px}</style></head><body>
<h1>KilatMart query federation</h1>
<p>Engine: DuckDB (mode: <code>{{.Mode}}</code>). Sources: orders in PostgreSQL, stock in MySQL. No data is copied: every report reads both databases live.</p>
<h2>Reports</h2><ul>
{{range .Reports}}<li><a href="/reports/{{.Name}}?format=html"><b>{{.Name}}</b></a> — {{.Description}}
 (<a href="/reports/{{.Name}}">json</a>, <a href="/reports/{{.Name}}?format=csv">csv</a>)</li>{{end}}
</ul>
<p><a href="/sources">Source status</a> · Ad-hoc SQL: <code>curl -X POST --data 'SELECT ...' localhost:8080/sql</code></p>
</body></html>`))

var reportTmpl = template.Must(template.New("report").Parse(`<!doctype html>
<html><head><meta charset="utf-8"><title>{{.Report}} · KilatMart</title>
<style>body{font-family:system-ui,sans-serif;margin:2rem;line-height:1.4}
table{border-collapse:collapse}td,th{border:1px solid #ccc;padding:.3rem .6rem;text-align:left}
th{background:#f2f2f2}.meta{color:#555}</style></head><body>
<p><a href="/">← reports</a></p>
<h1>{{.Report}}</h1>
<p class="meta">{{.RowCount}} rows · {{.ElapsedMS}} ms · generated {{.GeneratedAt.Format "2006-01-02 15:04:05"}} · engine {{.Engine}} ({{.Mode}})</p>
<table><tr>{{range .Columns}}<th>{{.}}</th>{{end}}</tr>
{{range .Rows}}<tr>{{range .}}<td>{{.}}</td>{{end}}</tr>{{end}}
</table></body></html>`))
