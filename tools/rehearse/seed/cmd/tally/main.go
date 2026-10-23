// Command tally prints the most frequent words on stdin.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"example.com/tally/tally"
)

func main() {
	n := flag.Int("n", 10, "how many words to print; negative prints all")
	flag.Parse()
	b, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	for _, c := range tally.Top(string(b), *n) {
		fmt.Printf("%d\t%s\n", c.N, c.Word)
	}
}
