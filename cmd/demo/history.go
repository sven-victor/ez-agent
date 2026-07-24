package main

import (
	"bufio"
	"os"
	"strings"
)

const maxInputHistory = 1000

func loadInputHistory(path string) []string {
	if path == "" {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	// Allow long pasted prompts.
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if line == "" {
			continue
		}
		out = append(out, line)
	}
	if len(out) > maxInputHistory {
		out = out[len(out)-maxInputHistory:]
	}
	return out
}

func saveInputHistory(path string, items []string) error {
	if path == "" {
		return nil
	}
	if len(items) > maxInputHistory {
		items = items[len(items)-maxInputHistory:]
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	for _, line := range items {
		// History file is line-oriented; collapse embedded newlines.
		line = strings.ReplaceAll(line, "\n", " ")
		if _, err := w.WriteString(line); err != nil {
			return err
		}
		if err := w.WriteByte('\n'); err != nil {
			return err
		}
	}
	return w.Flush()
}
