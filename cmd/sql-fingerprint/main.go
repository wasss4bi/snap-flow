package main

import (
	"fmt"
	"io"
	"os"
	"snap-flow-go/internal/observability"
)

func main() {
	data, err := io.ReadAll(io.LimitReader(os.Stdin, 16385))
	if err != nil || len(data) > 16384 {
		fmt.Fprintln(os.Stderr, "cannot read SQL or SQL exceeds 16 KiB")
		os.Exit(1)
	}
	fmt.Println(observability.SQLFingerprint(string(data)))
}
