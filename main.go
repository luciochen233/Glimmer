package main

import (
	"flag"
	"fmt"
	"log"
	"os"

	"golang.org/x/crypto/bcrypt"

	"urlshort/internal/config"
	"urlshort/internal/db"
	"urlshort/internal/server"
)

func main() {
	configPath := flag.String("config", "config.toml", "path to config file")
	hashPassword := flag.String("hash-password", "", "hash a password and print the bcrypt hash, then exit")
	flag.Parse()

	if *hashPassword != "" {
		hash, err := bcrypt.GenerateFromPassword([]byte(*hashPassword), bcrypt.DefaultCost)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(string(hash))
		return
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	database, err := db.Open(cfg.Database.Path)
	if err != nil {
		log.Fatalf("Failed to open database: %v", err)
	}
	defer database.Close()

	srv := server.New(cfg, database)
	if err := srv.Start(); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}
