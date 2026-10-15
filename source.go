// Package yamnaya carries ym's own source so a running ym can build itself for another host.
package yamnaya

import "embed"

//go:embed go.mod go.sum source.go cmd internal skill
var Source embed.FS
