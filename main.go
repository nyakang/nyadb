// NyaDB is a native desktop database workbench.
package main

import (
	"log"

	"github.com/nyakang/nyadb/internal/bootstrap"
)

func main() {
	if err := bootstrap.Run(); err != nil {
		log.Fatal(err)
	}
}
