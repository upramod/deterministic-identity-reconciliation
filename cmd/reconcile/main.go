package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/upramod/deterministic-identity-reconciliation/pkg/reconcile"
)

func main() {
	inputPath := flag.String("input", "examples/events.json", "path to a JSON array of physical events")
	nowValue := flag.String("now", "", "evaluation time in RFC3339 format; defaults to current UTC time")
	flag.Parse()

	events, err := readEvents(*inputPath)
	if err != nil {
		fail(err)
	}

	now := time.Now().UTC()
	if *nowValue != "" {
		now, err = time.Parse(time.RFC3339Nano, *nowValue)
		if err != nil {
			fail(fmt.Errorf("parse -now: %w", err))
		}
	}

	result, err := reconcile.ResolveBatch(events, reconcile.DefaultPolicy(now))
	if err != nil {
		fail(err)
	}

	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(result); err != nil {
		fail(fmt.Errorf("encode result: %w", err))
	}
}

func readEvents(path string) ([]reconcile.PhysicalEvent, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read input: %w", err)
	}
	var events []reconcile.PhysicalEvent
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&events); err != nil {
		return nil, fmt.Errorf("decode input: %w", err)
	}
	// Preserve the single-document constraint enforced by json.Unmarshal.
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("decode input: expected a single JSON document")
		}
		return nil, fmt.Errorf("decode input: %w", err)
	}
	return events, nil
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
