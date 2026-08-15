package main

import (
	"os"

	"ki/internal/cli"
)

func main() {
	os.Exit(cli.Main(os.Args[1:]))
}
