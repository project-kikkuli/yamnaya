# tally

A small Go project a yamnaya rehearsal hands to a real manager. `tally` prints the most frequent words on stdin.

    go run ./cmd/tally -n 5 < README.md

CI runs `go vet ./...` and `go test ./...` on every pull request and push to main.
