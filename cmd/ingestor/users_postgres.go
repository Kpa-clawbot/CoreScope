package main

import (
	"context"
	"database/sql"
	"strings"
)

// On PostgreSQL the accounts store is the corescope_users schema of the same
// database (internal/users.PostgresSchema). The ingestor reads proposals and
// companion_links there through its own connection, which holds only SELECT
// through the corescope_users_reader group. Like the SQLite path it never
// imports internal/users; TestUsersPGSchemaMatchesUsersPackage pins the name.

// usersPGSchema is the accounts schema. A variable only so a test can point
// the readers at a throwaway schema: the real one has a fixed name shared by
// every test on the database.
var usersPGSchema = "corescope_users"

// pgUsersQuery qualifies the table named after FROM with the accounts schema.
func pgUsersQuery(query, table string) string {
	return strings.Replace(query, "FROM "+table, `FROM "`+usersPGSchema+`".`+table, 1)
}

// queryUsersPG runs query in a READ ONLY transaction on db and scans one
// string column. READ ONLY is a second fence behind the role's grants: a code
// mistake here cannot write.
func queryUsersPG(db *sql.DB, query string, args ...any) ([]string, error) {
	tx, err := db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// isMissingTable reports a table that does not exist: SQLite's wording or
// PostgreSQL's undefined_table (a missing schema reads differently and is an
// ordinary failure).
func isMissingTable(err error) bool {
	if err == nil {
		return false
	}
	m := err.Error()
	return strings.Contains(m, "no such table") ||
		(strings.Contains(m, "relation ") && strings.Contains(m, "does not exist"))
}
