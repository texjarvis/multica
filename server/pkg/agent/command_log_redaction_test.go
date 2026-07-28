package agent

import (
	"bytes"
	"log/slog"
	"strconv"
	"strings"
	"testing"
)

func TestLogAgentCommandNeverLogsArgumentValues(t *testing.T) {
	const sentinel = "fixed-or-custom-arg-secret-sentinel"
	args := []string{
		"--profile",
		sentinel,
		"--config=token=" + sentinel,
		"user prompt " + sentinel,
	}
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	logAgentCommand(logger, "provider-cli", args)

	logged := buf.String()
	if strings.Contains(logged, sentinel) {
		t.Fatalf("agent command log exposed argv content: %s", logged)
	}
	if strings.Contains(logged, "args=") || strings.Contains(logged, "argv=") {
		t.Fatalf("agent command log exposed a raw argv field: %s", logged)
	}
	if !strings.Contains(logged, "arg_count="+strconv.Itoa(len(args))) {
		t.Fatalf("agent command log omitted safe arg_count metadata: %s", logged)
	}
}

func TestLogCopilotParseFailureNeverLogsLineContent(t *testing.T) {
	const sentinel = "copilot-unparseable-stdout-secret-sentinel"
	line := `{"custom_env":{"TOKEN":"` + sentinel + `"}`
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	logCopilotParseFailure(logger, line)

	logged := buf.String()
	if strings.Contains(logged, sentinel) || strings.Contains(logged, line) {
		t.Fatalf("copilot parse-failure log exposed stdout content: %s", logged)
	}
	if !strings.Contains(logged, "line_char_count="+strconv.Itoa(len(line))) {
		t.Fatalf("copilot parse-failure log omitted safe length metadata: %s", logged)
	}
}

func TestProviderStderrLogWriterNeverLogsChunkContent(t *testing.T) {
	const sentinel = "provider-stderr-secret-sentinel"
	chunk := []byte("failed with TOKEN=" + sentinel + "\n")
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	writer := newLogWriter(logger, "[provider:stderr] ")

	n, err := writer.Write(chunk)
	if err != nil || n != len(chunk) {
		t.Fatalf("Write = (%d, %v), want (%d, nil)", n, err, len(chunk))
	}
	logged := buf.String()
	if strings.Contains(logged, sentinel) || strings.Contains(logged, string(chunk)) {
		t.Fatalf("provider stderr log exposed chunk content: %s", logged)
	}
	if !strings.Contains(logged, "chunk_byte_count="+strconv.Itoa(len(chunk))) {
		t.Fatalf("provider stderr log omitted safe byte count: %s", logged)
	}
}

func TestProviderUnparsedOutputNeverLogsContent(t *testing.T) {
	const sentinel = "provider-unparsed-output-secret-sentinel"
	content := `{"custom_env":{"TOKEN":"` + sentinel + `"}`
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	logProviderUnparsedOutput(logger, "stdout", content)

	logged := buf.String()
	if strings.Contains(logged, sentinel) || strings.Contains(logged, content) {
		t.Fatalf("provider output log exposed content: %s", logged)
	}
	if !strings.Contains(logged, "content_char_count="+strconv.Itoa(len(content))) {
		t.Fatalf("provider output log omitted safe length metadata: %s", logged)
	}
}

func TestProviderStartedNeverLogsCwdOrModelValues(t *testing.T) {
	const sentinel = "provider-start-secret-sentinel"
	opts := ExecOptions{
		Cwd:   "/work/" + sentinel,
		Model: "model-" + sentinel,
	}
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	logProviderStarted(logger, "provider", 123, opts)

	logged := buf.String()
	if strings.Contains(logged, sentinel) ||
		strings.Contains(logged, opts.Cwd) ||
		strings.Contains(logged, opts.Model) {
		t.Fatalf("provider start log exposed cwd or model content: %s", logged)
	}
	if !strings.Contains(logged, "has_cwd=true") || !strings.Contains(logged, "has_model=true") {
		t.Fatalf("provider start log omitted safe presence metadata: %s", logged)
	}
}
