//go:build integration

package integration

import (
	"os"
	"strings"
)

// Where the test servers listen. The ports can be moved with the same
// variables compose.yaml reads, so that two stacks can run side by side.
var (
	dovecotPlainAddr = "127.0.0.1:" + testPort("PIM_MCP_TEST_IMAP_PORT", "31143")
	dovecotTLSAddr   = "127.0.0.1:" + testPort("PIM_MCP_TEST_IMAPS_PORT", "31993")
	radicaleURL      = "http://127.0.0.1:" + testPort("PIM_MCP_TEST_RADICALE_PORT", "35232") + "/"
	baikalURL        = "http://127.0.0.1:" + testPort("PIM_MCP_TEST_BAIKAL_PORT", "35233") + "/"
	nextcloudURL     = "http://127.0.0.1:" + testPort("PIM_MCP_TEST_NEXTCLOUD_PORT", "35234") + "/"
)

func testPort(variable, fallback string) string {
	if port := os.Getenv(variable); port != "" {
		return port
	}
	return fallback
}

// hostOf returns the host:port of one of the URLs above.
func hostOf(url string) string {
	return strings.TrimSuffix(strings.TrimPrefix(url, "http://"), "/")
}
