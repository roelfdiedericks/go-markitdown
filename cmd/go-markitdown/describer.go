package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
	"unicode"
)

// subprocessDescriber invokes an external command for each image, passing
// the image on stdin and the prompt/mime via environment variables. It
// implements docconv.ImageDescriber.
type subprocessDescriber struct {
	command []string
	timeout time.Duration
	verbose bool
}

// DescribeImage runs the configured command with the image on stdin. Returns
// the command's stdout as the description. Non-zero exit codes surface as
// errors; the library treats those as "use a placeholder" without aborting
// the whole document.
func (s *subprocessDescriber) DescribeImage(ctx context.Context, img []byte, mimeType string, prompt string) (string, error) {
	if len(s.command) == 0 {
		return "", fmt.Errorf("describer: empty command")
	}

	timeout := s.timeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, s.command[0], s.command[1:]...)
	cmd.Env = append(os.Environ(),
		"GO_MARKITDOWN_PROMPT="+prompt,
		"GO_MARKITDOWN_MIME="+mimeType,
	)
	cmd.Stdin = bytes.NewReader(img)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if s.verbose {
		fmt.Fprintf(os.Stderr, "go-markitdown: describer invoke (%d bytes, mime=%s, prompt=%q)\n",
			len(img), mimeType, truncateForLog(prompt, 60))
	}

	err := cmd.Run()

	if stderr.Len() > 0 {
		os.Stderr.Write(stderr.Bytes())
	}

	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("describer: timeout after %s", timeout)
		}
		return "", fmt.Errorf("describer: %w", err)
	}
	return strings.TrimSpace(stdout.String()), nil
}

// parseDescriberCommand splits a shell-style command into argv. Handles
// single and double quoted segments and backslash escaping. This is enough
// for the common cases (`./script.sh`, `python3 -u run.py`) without pulling
// in a full shell library.
func parseDescriberCommand(s string) ([]string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, fmt.Errorf("command is empty")
	}

	var (
		args    []string
		current strings.Builder
		quote   rune
		escaped bool
	)

	flush := func() {
		if current.Len() > 0 || quote != 0 {
			args = append(args, current.String())
			current.Reset()
		}
	}

	for _, r := range s {
		switch {
		case escaped:
			current.WriteRune(r)
			escaped = false
		case r == '\\' && quote != '\'':
			escaped = true
		case quote != 0:
			if r == quote {
				quote = 0
				// Force arg emission even if empty (e.g. "").
				if current.Len() == 0 {
					args = append(args, "")
				}
				continue
			}
			current.WriteRune(r)
		case r == '\'' || r == '"':
			quote = r
		case unicode.IsSpace(r):
			flush()
		default:
			current.WriteRune(r)
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("unterminated quote")
	}
	flush()

	if len(args) == 0 {
		return nil, fmt.Errorf("command is empty")
	}
	return args, nil
}

func truncateForLog(s string, max int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= max {
		return s
	}
	return s[:max-1] + "…"
}
