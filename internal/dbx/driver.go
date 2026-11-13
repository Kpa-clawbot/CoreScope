package dbx

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"

	"github.com/jackc/pgx/v5/stdlib"
)

// PostgresDriverName is the database/sql driver name this package registers
// (in init): pgx's stdlib driver with ? placeholders rewritten to $n.
const PostgresDriverName = "pgx-qmark"

func init() {
	sql.Register(PostgresDriverName, &qmarkDriver{inner: stdlib.GetDefaultDriver().(*stdlib.Driver)})
}

// qmarkDriver wraps pgx's stdlib driver. It implements driver.DriverContext
// so sql.Open goes through a Connector (as pgx's own driver does) and
// sql.OpenDB works with the connector returned by OpenConnector.
type qmarkDriver struct {
	inner *stdlib.Driver
}

var (
	_ driver.Driver        = (*qmarkDriver)(nil)
	_ driver.DriverContext = (*qmarkDriver)(nil)
)

func (d *qmarkDriver) Open(name string) (driver.Conn, error) {
	c, err := d.inner.Open(name)
	if err != nil {
		return nil, err
	}
	return wrapConn(c)
}

func (d *qmarkDriver) OpenConnector(name string) (driver.Connector, error) {
	c, err := d.inner.OpenConnector(name)
	if err != nil {
		return nil, err
	}
	return &qmarkConnector{inner: c, driver: d}, nil
}

type qmarkConnector struct {
	inner  driver.Connector
	driver *qmarkDriver
}

func (c *qmarkConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.inner.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return wrapConn(conn)
}

func (c *qmarkConnector) Driver() driver.Driver { return c.driver }

// wrapConn requires pgx's concrete *stdlib.Conn so that forwarding is checked
// by the compiler: if pgx ever drops one of the methods below, this package
// stops building instead of silently changing database/sql behaviour.
func wrapConn(c driver.Conn) (driver.Conn, error) {
	pc, ok := c.(*stdlib.Conn)
	if !ok {
		c.Close()
		return nil, fmt.Errorf("dbx: unexpected pgx connection type %T", c)
	}
	return &conn{inner: pc}, nil
}

// conn is a pgx stdlib connection whose query text is passed through Rebind.
// It must expose every optional interface database/sql probes for on the
// inner conn; TestConnImplementsOptionalInterfaces pins the list. Statements,
// rows and transactions are pgx's own and pass through untouched.
type conn struct {
	inner *stdlib.Conn
}

var (
	_ driver.Conn               = (*conn)(nil)
	_ driver.ConnBeginTx        = (*conn)(nil)
	_ driver.ConnPrepareContext = (*conn)(nil)
	_ driver.QueryerContext     = (*conn)(nil)
	_ driver.ExecerContext      = (*conn)(nil)
	_ driver.Pinger             = (*conn)(nil)
	_ driver.SessionResetter    = (*conn)(nil)
	_ driver.Validator          = (*conn)(nil)
	_ driver.NamedValueChecker  = (*conn)(nil)
)

func (c *conn) Prepare(query string) (driver.Stmt, error) {
	return c.inner.Prepare(Rebind(query))
}

func (c *conn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	return c.inner.PrepareContext(ctx, Rebind(query))
}

func (c *conn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	return c.inner.QueryContext(ctx, Rebind(query), args)
}

func (c *conn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	return c.inner.ExecContext(ctx, Rebind(query), args)
}

func (c *conn) Close() error { return c.inner.Close() }

// Begin is required by driver.Conn; database/sql itself calls BeginTx.
func (c *conn) Begin() (driver.Tx, error) { return c.inner.Begin() }

func (c *conn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	return c.inner.BeginTx(ctx, opts)
}

func (c *conn) Ping(ctx context.Context) error { return c.inner.Ping(ctx) }

func (c *conn) ResetSession(ctx context.Context) error { return c.inner.ResetSession(ctx) }

// CheckNamedValue must be forwarded: pgx accepts every argument as-is
// (slices, pgtype values, Valuers), whereas database/sql's default
// conversion would reject or coerce them.
func (c *conn) CheckNamedValue(nv *driver.NamedValue) error { return c.inner.CheckNamedValue(nv) }

// IsValid lets database/sql drop a connection pgx has closed (e.g. after a
// fatal error) instead of returning it to the pool. pgx's stdlib Conn does not
// implement driver.Validator itself, so this is derived from its pgx.Conn.
func (c *conn) IsValid() bool { return !c.inner.Conn().IsClosed() }
