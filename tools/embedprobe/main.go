// embedprobe checks that an OpenAI-compatible embeddings endpoint answers,
// and says how to get one when it does not.
//
// It replaces a shell conditional in the Taskfile (curl, a redirection, an
// if, four echoes and an exit) that the no-shell guard did not read, because
// the guard read `cmd:` mappings and this was a `- |` block. Found by the
// pre-release review, round eleven.
package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

func main() {
	url := flag.String("url", "http://127.0.0.1:11434", "the service's base URL")
	model := flag.String("model", "qwen3-embedding:0.6b", "the embeddings model to ask for")
	flag.Parse()
	if err := probe(*url, *model); err != nil {
		fmt.Fprintf(os.Stderr, "no embeddings service at %s (%v)\n"+
			"  ollama:  brew install ollama, then ollama serve, then ollama pull %s\n"+
			"  or set EMBED_URL/EMBED_MODEL to any OpenAI-compatible endpoint\n",
			*url, err, *model)
		os.Exit(1)
	}
}

func probe(url, model string) error {
	body := fmt.Sprintf(`{"model":%q,"input":["probe"]}`, model)
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Post(strings.TrimRight(url, "/")+"/v1/embeddings", "application/json", strings.NewReader(body))
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("HTTP %s", resp.Status)
	}
	return nil
}
