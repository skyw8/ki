package llmprotocol

import (
	"context"
	"errors"
	"io"
	"net/http"
	"testing"
	"testing/synctest"
	"time"
)

func TestStreamIdleKeepsPartialAndDoesNotRetry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		reader, writer := io.Pipe()
		defer writer.Close()
		client := NewClient(APICompletions, "https://example.test", "", testDoer(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: reader, Header: make(http.Header)}, nil
		}))
		client.IdleTimeout = time.Second
		go func() {
			_, _ = io.WriteString(writer, "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n")
		}()
		message, err := client.Stream(context.Background(), Request{Model: "m"}, nil)
		var terminal interface{ NonRetryable() bool }
		if !errors.Is(err, errStreamIdle) || !errors.As(err, &terminal) || !terminal.NonRetryable() || message.Text() != "partial" {
			t.Fatalf("partial=%q error=%v", message.Text(), err)
		}
	})
}

func TestStreamIdleCountsHeartbeatBytesAndExcludesConsumerTime(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		reader, writer := io.Pipe()
		client := NewClient(APICompletions, "https://example.test", "", testDoer(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: reader, Header: make(http.Header)}, nil
		}))
		client.IdleTimeout = time.Second
		go func() {
			defer writer.Close()
			for range 5 {
				time.Sleep(500 * time.Millisecond)
				_, _ = io.WriteString(writer, ": ping\n\n")
			}
			_, _ = io.WriteString(writer, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n")
			_, _ = io.WriteString(writer, "data: [DONE]\n\n")
		}()
		message, err := client.Stream(context.Background(), Request{Model: "m"}, func(AssistantDelta) error {
			time.Sleep(3 * time.Second)
			return nil
		})
		if err != nil || message.Text() != "ok" {
			t.Fatalf("partial=%q error=%v", message.Text(), err)
		}
	})
}
