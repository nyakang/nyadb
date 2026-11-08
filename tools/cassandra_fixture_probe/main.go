// The fixture probe accepts only private local file paths; never put credentials
// on a process command line. It is a development utility, not a public provider.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/nyakang/nyadb/internal/services/cassandra"
	"github.com/nyakang/nyadb/internal/services/connections"
	"os"
	"strings"
	"time"
)

func main() {
	config := flag.String("config", "", "private probe configuration")
	next := flag.String("new-password-file", "", "one-shot owned fixture password bootstrap")
	flag.Parse()
	raw, err := os.ReadFile(*config)
	if err != nil {
		os.Exit(1)
	}
	var cfg struct {
		Endpoint, Auth, Username, TLSMode string
		CAFile                            string `json:"ca_file"`
		ServerName                        string `json:"server_name"`
		PasswordFile                      string `json:"password_file"`
	}
	if json.Unmarshal(raw, &cfg) != nil {
		os.Exit(1)
	}
	secret, err := os.ReadFile(cfg.PasswordFile)
	if err != nil {
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c, err := cassandra.Open(ctx, connections.CassandraConfig{Endpoint: cfg.Endpoint, Auth: cfg.Auth, Username: cfg.Username, TLSMode: cfg.TLSMode, CAFile: cfg.CAFile, ServerName: cfg.ServerName}, string(secret))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer c.Close()
	if *next != "" {
		value, err := os.ReadFile(*next)
		if err != nil {
			os.Exit(1)
		}
		if strings.ContainsAny(string(value), "'\x00\r\n") || len(value) == 0 {
			os.Exit(1)
		}
		result, err := c.Execute(ctx, cassandra.Request{Statement: "ALTER ROLE cassandra WITH PASSWORD = '" + string(value) + "'"})
		if err != nil || result.Kind != 1 {
			fmt.Fprintln(os.Stderr, "owned bootstrap failed")
			os.Exit(1)
		}
	}
	fmt.Println(c.Identity().ReleaseVersion)
}
