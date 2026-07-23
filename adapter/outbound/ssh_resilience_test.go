package outbound

import (
	"os"
	"strings"
	"testing"
)

func TestStartHealthCheckDoesNotStartSecondWaitGoroutine(t *testing.T) {
	source, err := os.ReadFile("ssh_resilience.go")
	if err != nil {
		t.Fatal(err)
	}

	body := string(source)
	start := strings.Index(body, "func (s *Ssh) startHealthCheck")
	if start == -1 {
		t.Fatal("startHealthCheck not found")
	}
	end := strings.Index(body[start:], "// isClosed")
	if end == -1 {
		t.Fatal("startHealthCheck end marker not found")
	}
	body = body[start : start+end]

	if strings.Contains(body, "client.Wait()") {
		t.Fatal("startHealthCheck calls client.Wait")
	}
}
