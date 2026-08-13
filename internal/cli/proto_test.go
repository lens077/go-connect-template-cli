package cli

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunProtoGenForceDoesNotDuplicateWiring(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "go.mod"), []byte("module github.com/acme/shop\n\ngo 1.26.1\n"), 0o644))

	protoDir := filepath.Join(root, "api", "cart", "v1")
	require.NoError(t, os.MkdirAll(protoDir, 0o755))
	protoPath := filepath.Join(protoDir, "cart.proto")
	const proto = `syntax = "proto3";
package cart.v1;
option go_package = "github.com/acme/shop/api/cart/v1;cartv1";
service CartService { rpc GetCart(GetCartRequest) returns (GetCartResponse) {} }
message GetCartRequest {}
message GetCartResponse {}
`
	require.NoError(t, os.WriteFile(protoPath, []byte(proto), 0o644))

	anchors := map[string]string{
		"internal/biz/biz.go":         "package biz\n\nvar Module = fx.Provide(\n\t// +co:anchor biz-providers\n)\n",
		"internal/data/data.go":       "package data\n\nvar Module = fx.Provide(\n\t// +co:anchor data-providers\n)\n",
		"internal/service/service.go": "package service\n\nvar Module = fx.Provide(\n\t// +co:anchor service-providers\n)\n",
		"internal/server/server.go": `package server

import (
	// +co:anchor server-imports
)

func New(
	// +co:anchor server-handler-params
) {
	// +co:anchor server-handler-register
}
`,
	}
	for rel, content := range anchors {
		path := filepath.Join(root, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	}

	cmd := &cobra.Command{}
	cmd.SetOut(io.Discard)
	require.NoError(t, runProtoGen(cmd, protoPath, root, "", true, false, false))
	first := readAnchorFiles(t, root, anchors)
	require.NoError(t, runProtoGen(cmd, protoPath, root, "", true, false, false))
	second := readAnchorFiles(t, root, anchors)

	assert.Equal(t, first, second)
}

func readAnchorFiles(t *testing.T, root string, files map[string]string) map[string]string {
	t.Helper()
	out := make(map[string]string, len(files))
	for rel := range files {
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		require.NoError(t, err)
		out[rel] = string(data)
	}
	return out
}
