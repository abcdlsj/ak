package main

import (
	"fmt"
	"os"

	"github.com/abcdlsj/ak/internal/cli"
)

func main() {
	if err := cli.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "ak: %v\n", err)
		os.Exit(1)
	}
}
