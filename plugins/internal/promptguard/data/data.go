// Package data embeds the trained prompt-guard model. Rebuild it with
//
//	go run ./plugins/internal/cmd/prompt-guard-corpus render
//	go run ./plugins/internal/cmd/prompt-guard-corpus train -fit all
package data

import _ "embed"

// RawModel is the serialised logistic regression, see promptguard.LoadModel.
//
//go:embed model.json
var RawModel []byte
