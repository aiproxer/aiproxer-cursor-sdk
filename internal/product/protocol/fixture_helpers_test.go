package protocol_test

import (
	"testing"

	"github.com/aiproxer/aiproxer-cursor-sdk/internal/product/protocol"
)

func mustFixtureRoot(t *testing.T) string {
	t.Helper()
	root, err := protocol.FixtureRoot()
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func mustReadFixture(t *testing.T, rel string) []byte {
	t.Helper()
	raw, err := protocol.ReadFixtureBytes(rel)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
