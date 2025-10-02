package main

import (
	"database/sql"
	"log"
	"os"

	_ "github.com/mattn/go-sqlite3"
)

var DB *sql.DB

func initDB() {
	var err error
	DB, err = sql.Open("sqlite3", "file:vault.db?_busy_timeout=5000&_foreign_keys=on")
	if err != nil {
		log.Fatal(err)
	}
	if _, err := os.Stat("schema.sql"); err == nil {
		b, _ := os.ReadFile("schema.sql")
		if _, err := DB.Exec(string(b)); err != nil {
			log.Fatal(err)
		}
	}
}
