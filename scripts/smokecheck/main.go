// Command smokecheck prints row counts for the raw event tables of an
// analytics database. Used by scripts/smoke.sh to verify ingestion
// end-to-end without requiring a sqlite3 CLI on the host.
package main

import (
	"database/sql"
	"fmt"
	"os"

	_ "modernc.org/sqlite"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: smokecheck <db-path>")
		os.Exit(2)
	}
	db, err := sql.Open("sqlite", "file:"+os.Args[1]+"?mode=ro")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer db.Close()
	count := func(table string) int {
		var n int
		if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return n
	}
	fmt.Printf("web=%d app=%d product=%d\n",
		count("web_hits"), count("app_views"), count("product_events"))
}
