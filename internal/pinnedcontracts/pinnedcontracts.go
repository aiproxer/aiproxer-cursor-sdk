// Package pinnedcontracts is this module's compile-time assertion of the
// published Go-LIP contract baseline.
//
// The standalone Cursor SDK plugin depends only on released Go-LIP modules:
// the root module and the ACP connector-support module. This package imports
// their public contract packages for the side effect of compiling, so that
//
//   - go build ./... and go test ./... prove the pinned versions resolve from
//     a clean checkout, without a sibling Go-LIP checkout, a workspace file,
//     or a replace directive;
//   - go mod tidy keeps both requirements honest instead of pruning them.
//
// It intentionally contains no Cursor provider behavior, no Cursor SDK
// access, and no stub of the plugin's eventual service implementation.
package pinnedcontracts

import (
	_ "github.com/matdev83/go-llm-interactive-proxy/connector-support/acp"
	_ "github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	_ "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	_ "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin"
	_ "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/modelinventory"
)
