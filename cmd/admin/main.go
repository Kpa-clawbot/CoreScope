// Command admin manages CoreScope admin accounts directly against
// admin.db. It exists because admin accounts have no public
// registration flow (by design) — an operator runs this once, via
// `docker exec`, to bootstrap the first super-admin. From then on,
// super-admins can create further admin accounts through the web UI at
// /admin.
//
// Usage:
//
//	admin -db path/to/admin.db create-super-admin -username <name>
//	admin -db path/to/admin.db list
//
// create-super-admin prompts for the password twice on the terminal
// (hidden input) rather than accepting it as a flag or argument, so it
// never lands in shell history or `ps` output.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/meshcore-analyzer/admindb"
	"golang.org/x/term"
)

func main() {
	dbPath := flag.String("db", "", "path to admin.db (required)")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage:\n")
		fmt.Fprintf(os.Stderr, "  %s -db <path> create-super-admin -username <name>\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "  %s -db <path> list\n", os.Args[0])
	}
	flag.Parse()

	if *dbPath == "" {
		fmt.Fprintln(os.Stderr, "[admin] -db is required")
		flag.Usage()
		os.Exit(2)
	}

	args := flag.Args()
	if len(args) == 0 {
		flag.Usage()
		os.Exit(2)
	}

	log.SetFlags(0)
	log.SetPrefix("[admin] ")

	store, err := admindb.Open(*dbPath)
	if err != nil {
		log.Fatalf("open %s: %v", *dbPath, err)
	}
	defer store.Close()

	switch cmd := args[0]; cmd {
	case "create-super-admin":
		runCreateSuperAdmin(store, args[1:])
	case "list":
		runList(store)
	default:
		fmt.Fprintf(os.Stderr, "[admin] unknown command %q\n", cmd)
		flag.Usage()
		os.Exit(2)
	}
}

func runCreateSuperAdmin(store *admindb.Store, args []string) {
	fs := flag.NewFlagSet("create-super-admin", flag.ExitOnError)
	username := fs.String("username", "", "username for the new super-admin (required)")
	fs.Parse(args)

	if *username == "" {
		fmt.Fprintln(os.Stderr, "[admin] -username is required")
		os.Exit(2)
	}

	password, err := readPasswordTwice()
	if err != nil {
		log.Fatalf("read password: %v", err)
	}

	a, err := store.CreateAdmin(*username, password, admindb.RoleSuperAdmin, nil)
	if err != nil {
		log.Fatalf("create super-admin: %v", err)
	}
	log.Printf("created super-admin %q (id=%d)", a.Username, a.ID)
}

func runList(store *admindb.Store) {
	admins, err := store.ListAdmins()
	if err != nil {
		log.Fatalf("list admins: %v", err)
	}
	if len(admins) == 0 {
		fmt.Println("(no admins yet)")
		return
	}
	w := bufio.NewWriter(os.Stdout)
	defer w.Flush()
	fmt.Fprintf(w, "%-24s %-14s %-8s %s\n", "USERNAME", "ROLE", "DISABLED", "CREATED_AT")
	for _, a := range admins {
		fmt.Fprintf(w, "%-24s %-14s %-8t %s\n", a.Username, a.Role, a.Disabled, a.CreatedAt.Format("2006-01-02 15:04:05"))
	}
}

// readPasswordTwice prompts for a password twice (hidden input) and
// requires the two entries to match.
func readPasswordTwice() (string, error) {
	fmt.Fprint(os.Stderr, "Password: ")
	p1, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", fmt.Errorf("read password: %w", err)
	}
	if len(p1) == 0 {
		return "", fmt.Errorf("password must not be empty")
	}

	fmt.Fprint(os.Stderr, "Confirm password: ")
	p2, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", fmt.Errorf("read password confirmation: %w", err)
	}

	if string(p1) != string(p2) {
		return "", fmt.Errorf("passwords did not match")
	}
	return string(p1), nil
}
