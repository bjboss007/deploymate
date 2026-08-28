// Command dbprobe is the P3 end-to-end fixture: a tiny HTTP server that
// connects to Postgres via the injected DATABASE_URL and reports the result
// of a query, retrying while the database starts up.
package main

import (
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	_ "github.com/lib/pq"
)

func main() {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		log.Fatal("DATABASE_URL not set — is a Postgres service running in this project?")
	}

	// Wait for the database to accept connections (it may still be booting
	// when our container starts).
	var db *sql.DB
	for i := 0; i < 30; i++ {
		var err error
		db, err = sql.Open("postgres", url)
		if err == nil {
			if err = db.Ping(); err == nil {
				break
			}
		}
		log.Printf("waiting for postgres (%d/30): %v", i+1, err)
		time.Sleep(2 * time.Second)
	}
	if db == nil {
		log.Fatal("postgres never became ready")
	}

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		var n int
		if err := db.QueryRow("SELECT 42").Scan(&n); err != nil {
			fmt.Fprintf(w, "query failed: %v", err)
			return
		}
		fmt.Fprintf(w, "postgres says: %d", n)
	})
	log.Println("dbprobe listening on :8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}
