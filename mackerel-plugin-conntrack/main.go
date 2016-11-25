package main

import (
	"os"
)

// Name is executable name of this application.
const Name string = "mackerel-plugin-conntrack"

// Version is version string of this application.
const Version string = "0.1.0"

func main() {
	cli := &CLI{
		outStream: os.Stdout,
		errStream: os.Stderr,
	}
	os.Exit(cli.Run(os.Args))
}
