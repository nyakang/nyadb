// NyaDB is a native desktop database workbench.
package main

import (
	"log"

	"github.com/nyakang/nyadb/internal/bootstrap"
	"github.com/nyakang/nyadb/internal/services/dameng/dpiworker"
)

func main() {
	if dpiworker.Dispatch() {
		return
	}
	if err := bootstrap.Run(); err != nil {
		log.Fatal(err)
	}
}
