// Package main is the entrypoint for the squirrel image-rewrite operator.
//
// The operator is implemented in stages; the wiring lives here once the
// component packages are in place. Until then this file establishes the
// build target so `go build ./...` and golangci-lint have something to
// chew on. See docs/design/v1alpha1.md for the design.
package main

func main() {
}
