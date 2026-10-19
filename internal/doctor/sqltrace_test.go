package doctor

import (
	"database/sql"
	"database/sql/driver"
	"regexp"
	"sync"
	"testing"

	"github.com/linhn0617/clio/internal/db"
)

// traceDriverName is a sqlite driver that records every statement prepared on any
// of its connections. The wrapped conn exposes only driver.Conn, so database/sql
// sends every Query, QueryRow and Exec through Prepare: nothing runs unseen.
const traceDriverName = "sqlite-trace"

var trace struct {
	sync.Mutex
	stmts []string
}

type traceDriver struct{ base driver.Driver }

func (d traceDriver) Open(name string) (driver.Conn, error) {
	c, err := d.base.Open(name)
	if err != nil {
		return nil, err
	}
	return traceConn{c}, nil
}

type traceConn struct{ driver.Conn }

func (c traceConn) Prepare(query string) (driver.Stmt, error) {
	trace.Lock()
	trace.stmts = append(trace.stmts, query)
	trace.Unlock()
	return c.Conn.Prepare(query)
}

func init() {
	base, err := sql.Open("sqlite", "")
	if err != nil {
		panic(err)
	}
	sql.Register(traceDriverName, traceDriver{base.Driver()})
	base.Close()
}

// heavyScanRe matches every whole-database scan doctor knows of, in any spelling
// SQLite accepts (PRAGMA x, pragma_x() table functions, the FTS5 command).
var heavyScanRe = regexp.MustCompile(`(?i)integrity_check|quick_check|integrity-check|dbstat`)

// tracedRun runs RunWith on a read-only traced connection to dbPath (the way the
// CLI opens it), with doctor's own writable connection traced too, and returns the
// results and every heavy-scan statement that reached SQLite.
func tracedRun(t *testing.T, dir, dbPath string, opts Options) ([]Result, []string) {
	t.Helper()
	conn, err := sql.Open(traceDriverName, "file:"+dbPath+"?_pragma=busy_timeout(3000)&mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	orig := sqliteDriverName
	sqliteDriverName = traceDriverName
	defer func() { sqliteDriverName = orig }()

	trace.Lock()
	trace.stmts = nil
	trace.Unlock()
	res := RunWith(&db.DB{DB: conn}, dir, dbPath, opts)
	trace.Lock()
	defer trace.Unlock()
	var heavy []string
	for _, s := range trace.stmts {
		if heavyScanRe.MatchString(s) {
			heavy = append(heavy, s)
		}
	}
	if len(trace.stmts) == 0 {
		t.Fatal("trace recorded nothing: the driver hook is not wired")
	}
	return res, heavy
}
