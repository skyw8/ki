import base64
import io
import json
import threading
import time
import unittest
import urllib.error
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from unittest import mock

import main


def jwt_for_account(account: str) -> str:
    encode = lambda value: base64.urlsafe_b64encode(json.dumps(value).encode()).rstrip(b"=").decode()
    return encode({"alg": "none"}) + "." + encode({"https://api.openai.com/auth": {"chatgpt_account_id": account}}) + ".signature"


class CodexExtensionTest(unittest.TestCase):
    def test_auth_input_and_account_id(self):
        self.assertEqual(main.parse_auth_input("abc#xyz"), ("abc", "xyz"))
        self.assertEqual(main.parse_auth_input("http://localhost/callback?code=abc&state=xyz"), ("abc", "xyz"))
        self.assertEqual(main.account_id(jwt_for_account("acct")), "acct")

    def test_build_request_preserves_replay_metadata(self):
        payload = {
            "model": {"id": "gpt-5.4", "input": ["text"], "api": "openai-codex-responses"},
            "request": {
                "system": "system",
                "sessionId": "session-1",
                "maxTokens": 128000,
                "thinkingEffort": "high",
                "thinkingLevelMap": {"high": "xhigh"},
                "messages": [
                    {"role": "user", "content": [{"type": "text", "text": "run"}]},
                    {"role": "assistant", "content": [{"type": "toolCall", "id": "call-1", "itemId": "fc-1", "name": "run", "arguments": {"x": 1}}]},
                    {"role": "toolResult", "toolCallId": "call-1", "toolType": "function", "content": [{"type": "text", "text": "ok"}]},
                ],
                "tools": [{"name": "run", "description": "run", "parameters": {"type": "object"}}],
            },
        }
        body = main.build_request(payload)
        self.assertEqual(body["prompt_cache_key"], "session-1")
        self.assertNotIn("max_output_tokens", body)
        self.assertEqual(body["reasoning"]["effort"], "xhigh")
        self.assertNotIn("strict", body["tools"][0])
        self.assertEqual(body["input"][1]["id"], "fc-1")
        self.assertEqual(body["input"][2]["type"], "function_call_output")
        self.assertEqual(body["input"][2]["call_id"], "call-1")

    def test_build_compact_request_appends_trigger_after_opaque_context(self):
        checkpoint = {
            "type": "compaction",
            "id": "cmp-old",
            "encrypted_content": "opaque-old",
            "future": {"nested": [1, 2, 3]},
        }
        payload = {
            "model": {"id": "gpt-5.6", "input": ["text"]},
            "request": {
                "responsesContext": [checkpoint],
                "messages": [{"role": "user", "content": [{"type": "text", "text": "continue"}]}],
            },
        }
        body = main.build_compact_request(payload)
        self.assertEqual(body["input"][0], checkpoint)
        self.assertIsNot(body["input"][0], checkpoint)
        self.assertEqual(body["input"][1]["type"], "message")
        self.assertEqual(body["input"][-1], {"type": "compaction_trigger"})

    def test_build_request_accepts_opaque_context_without_portable_messages(self):
        checkpoint = {"type": "compaction", "encrypted_content": "opaque"}
        body = main.build_request({
            "model": {"id": "gpt-5.6"},
            "request": {"responsesContext": [checkpoint], "messages": []},
        })
        self.assertEqual(body["input"], [checkpoint])

    def test_build_request_rejects_invalid_opaque_context(self):
        with self.assertRaisesRegex(RuntimeError, "array of objects"):
            main.build_request({
                "model": {"id": "gpt-5.6"},
                "request": {"responsesContext": ["not-an-object"], "messages": []},
            })

    def test_build_request_rejects_tool_result_without_call_id(self):
        payload = {
            "model": {"id": "gpt-5.4"},
            "request": {
                "messages": [
                    {"role": "user", "content": [{"type": "text", "text": "run"}]},
                    {"role": "toolResult", "content": [{"type": "text", "text": "ok"}]},
                ],
            },
        }
        with self.assertRaisesRegex(RuntimeError, "no toolCallId"):
            main.build_request(payload)

    def test_build_request_replays_encrypted_reasoning_item(self):
        payload = {
            "model": {"id": "gpt-5.6", "input": ["text"]},
            "request": {
                "messages": [
                    {"role": "user", "content": [{"type": "text", "text": "think"}]},
                    {"role": "assistant", "content": [
                        {"type": "thinking", "thinking": "summary", "thinkingSignature": json.dumps({"type": "reasoning", "id": "rs-1", "encrypted_content": "opaque"})},
                        {"type": "text", "text": "answer"},
                    ]},
                ],
            },
        }
        body = main.build_request(payload)
        reasoning = body["input"][1]
        self.assertEqual(reasoning["type"], "reasoning")
        self.assertEqual(reasoning["id"], "rs-1")
        self.assertEqual(reasoning["encrypted_content"], "opaque")

    def test_build_request_deduplicates_reasoning_items(self):
        signature = json.dumps({"type": "reasoning", "id": "rs-dup", "encrypted_content": "opaque"})
        payload = {
            "model": {"id": "gpt-5.6", "input": ["text"]},
            "request": {
                "messages": [
                    {"role": "user", "content": [{"type": "text", "text": "run"}]},
                    {"role": "assistant", "content": [
                        {"type": "thinking", "thinking": "summary", "thinkingSignature": signature},
                        {"type": "thinking", "thinking": "summary", "name": "Grep", "arguments": {}, "thinkingSignature": signature},
                        {"type": "toolCall", "id": "call-1", "name": "Grep", "arguments": {}},
                    ]},
                    {"role": "toolResult", "toolCallId": "call-1", "content": [{"type": "text", "text": "ok"}]},
                ],
            },
        }
        body = main.build_request(payload)
        self.assertEqual([item["type"] for item in body["input"]], ["message", "reasoning", "function_call", "function_call_output"])

    def test_slot_registry_prefers_item_id_over_reused_output_index(self):
        slots = main.SlotRegistry()
        first = slots.resolve({"output_index": 0}, "rs-1", "")
        second = slots.resolve({"output_index": 0}, "fc-1", "call-1")
        self.assertNotEqual(first, second)
        # A delta without an item ID must reach an item the output item events
        # registered, not invent a third slot: call_id is exact, and the output
        # index falls back to the most recent registration (reuse makes it
        # ambiguous, so the latest item wins).
        self.assertEqual(slots.resolve({"output_index": 0}, "", "call-1"), second)
        self.assertEqual(slots.resolve({"output_index": 0}, "", ""), second)
        # call_id is still usable when nothing registered it.
        self.assertEqual(slots.resolve({"output_index": 7}, "", "call-9"), "call:call-9")
        self.assertEqual(slots.resolve({"output_index": 7}, "", ""), "index:7")
        self.assertEqual(slots.resolve({}, "", ""), "item:unknown")

    def test_build_request_rejects_empty_input(self):
        payload = {"model": {"id": "gpt-5.4"}, "request": {"messages": []}}
        with self.assertRaisesRegex(RuntimeError, "no input messages"):
            main.build_request(payload)

    def test_replayable_synthetic_tool_result_uses_call_tool_id(self):
        payload = {
            "model": {"id": "gpt-5.4"},
            "request": {
                "messages": [
                    {"role": "user", "content": [{"type": "text", "text": "run"}]},
                    {"role": "assistant", "content": [{"type": "toolCall", "id": "call-1", "name": "run", "arguments": {}}]},
                ],
            },
        }
        body = main.build_request(payload)
        self.assertEqual(body["input"][-1]["type"], "function_call_output")
        self.assertEqual(body["input"][-1]["call_id"], "call-1")
        self.assertEqual(body["input"][-1]["output"], "No result provided")

    def test_replayable_repairs_legacy_empty_tool_ids(self):
        payload = {
            "model": {"id": "gpt-5.4"},
            "request": {
                "messages": [
                    {"role": "user", "content": [{"type": "text", "text": "run"}]},
                    {"role": "assistant", "content": [{"type": "toolCall", "id": "", "itemId": "fc-old", "name": "run", "arguments": {}}]},
                    {"role": "toolResult", "toolCallId": "", "content": []},
                ],
            },
        }
        body = main.build_request(payload)
        self.assertTrue(body["input"][1]["call_id"])
        self.assertEqual(body["input"][1]["call_id"], body["input"][2]["call_id"])

    def test_build_request_skips_empty_user_messages(self):
        payload = {"model": {"id": "gpt-5.4"}, "request": {"messages": [{"role": "user", "content": []}]}}
        with self.assertRaisesRegex(RuntimeError, "no input messages"):
            main.build_request(payload)

    def test_http_error_includes_upstream_response_body(self):
        class Handler(BaseHTTPRequestHandler):
            def do_POST(self):  # noqa: N802
                body = b'{"error":{"message":"unsupported parameter: max_output_tokens"}}'
                self.send_response(400)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)

            def log_message(self, *_args):
                return

        server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        threading.Thread(target=server.serve_forever, daemon=True).start()
        try:
            payload = {
                "model": {"id": "gpt-5.4", "baseUrl": f"http://127.0.0.1:{server.server_port}"},
                "credential": {"value": {"access": "access", "accountId": "acct"}},
                "request": {"messages": [{"role": "user", "content": [{"type": "text", "text": "hi"}]}]},
            }
            with self.assertRaises(urllib.error.HTTPError) as caught:
                main.stream_codex(threading.Event(), payload, lambda _event: None, "stream-1")
            self.assertIn("unsupported parameter", main.safe_error(caught.exception))
        finally:
            server.shutdown()
            server.server_close()

    def test_stream_sse(self):
        class Handler(BaseHTTPRequestHandler):
            def do_POST(self):  # noqa: N802
                self.send_response(200)
                self.send_header("Content-Type", "text/event-stream")
                self.end_headers()
                self.wfile.write(b'data: {"type":"response.created","response":{"id":"resp-1"}}\n\n')
                self.wfile.write(b'data: {"type":"response.output_item.added","item":{"type":"message","id":"msg-1"}}\n\n')
                self.wfile.write(b'data: {"type":"response.output_text.delta","item_id":"msg-1","delta":"hello"}\n\n')
                self.wfile.write(b'data: {"type":"response.completed","response":{"status":"completed"}}\n\n')

            def log_message(self, *_args):
                return

        server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        threading.Thread(target=server.serve_forever, daemon=True).start()
        try:
            events = []
            payload = {
                "model": {"id": "gpt-5.4", "provider": "openai-codex", "api": "openai-codex-responses", "baseUrl": f"http://127.0.0.1:{server.server_port}", "input": ["text"]},
                "credential": {"type": "oauth", "value": {"access": "access", "accountId": "acct"}},
                "request": {"messages": [{"role": "user", "content": [{"type": "text", "text": "hi"}]}]},
            }
            main.stream_codex(threading.Event(), payload, events.append, "stream-1")
            types = [event["params"]["type"] for event in events]
            self.assertEqual(types, ["start", "text_start", "text_delta", "done"])
            self.assertEqual(events[-1]["params"]["message"]["responseId"], "resp-1")
        finally:
            server.shutdown()
            server.server_close()

    def test_remote_compaction_v2_uses_responses_and_preserves_opaque_item(self):
        seen = {}
        compacted = {
            "type": "compaction",
            "id": "cmp-new",
            "encrypted_content": "opaque-new",
            "future_field": {"keep": ["all", "fields"]},
        }

        class Handler(BaseHTTPRequestHandler):
            def do_POST(self):  # noqa: N802
                seen["path"] = self.path
                seen["headers"] = {key.lower(): value for key, value in self.headers.items()}
                seen["body"] = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
                self.send_response(200)
                self.send_header("Content-Type", "text/event-stream")
                self.end_headers()
                events = [
                    {"type": "response.output_item.done", "output_index": 0, "item": compacted},
                    {
                        "type": "response.completed",
                        "response": {
                            "id": "resp-compact",
                            "status": "completed",
                            # The real backend completes a compaction response
                            # with an empty output array; the compaction item is
                            # only delivered via response.output_item.done.
                            "output": [],
                            "usage": {
                                "input_tokens": 20,
                                "output_tokens": 5,
                                "total_tokens": 25,
                                "input_tokens_details": {"cached_tokens": 7, "cache_write_tokens": 3},
                            },
                        },
                    },
                ]
                for event in events:
                    self.wfile.write(("data: " + json.dumps(event) + "\n\n").encode())

            def log_message(self, *_args):
                return

        server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        threading.Thread(target=server.serve_forever, daemon=True).start()
        try:
            old_checkpoint = {"type": "compaction", "encrypted_content": "opaque-old", "unknown": True}
            payload = {
                "provider": "openai-codex",
                "model": {
                    "id": "gpt-5.6",
                    "provider": "openai-codex",
                    "api": "openai-codex-responses",
                    "baseUrl": f"http://127.0.0.1:{server.server_port}",
                    "input": ["text"],
                },
                "credential": {"type": "oauth", "value": {"access": "access", "accountId": "acct"}},
                "request": {
                    "sessionId": "session-compact",
                    "responsesContext": [old_checkpoint],
                    "messages": [{"role": "user", "content": [{"type": "text", "text": "latest"}]}],
                },
            }
            result = main.compact_codex(threading.Event(), payload)
            self.assertEqual(seen["path"], "/codex/responses")
            self.assertEqual(seen["body"]["input"][0], old_checkpoint)
            self.assertEqual(seen["body"]["input"][-1], {"type": "compaction_trigger"})
            self.assertNotIn("/compact", seen["path"])
            self.assertEqual(seen["headers"]["chatgpt-account-id"], "acct")
            # The backend only honors the appended compaction_trigger for a
            # client that advertises the Remote Compaction V2 beta feature and
            # carries the Codex client identity.
            self.assertEqual(seen["headers"]["x-codex-beta-features"], "remote_compaction_v2")
            self.assertEqual(seen["headers"]["originator"], "codex_cli_rs")
            self.assertTrue(seen["headers"]["user-agent"].startswith("codex_cli_rs/"))
            metadata = json.loads(seen["headers"]["x-codex-turn-metadata"])
            self.assertEqual(metadata["request_kind"], "compaction")
            self.assertEqual(metadata["compaction"]["implementation"], "responses_compaction_v2")
            self.assertEqual(metadata["compaction"]["phase"], "standalone_turn")
            self.assertEqual(metadata["window_id"], seen["headers"]["x-codex-window-id"])
            self.assertEqual(metadata["root_turn_id"], metadata["turn_id"])
            client_metadata = seen["body"]["client_metadata"]
            self.assertEqual(client_metadata["x-codex-window-id"], metadata["window_id"])
            self.assertEqual(client_metadata["x-codex-turn-metadata"], seen["headers"]["x-codex-turn-metadata"])
            self.assertEqual(result["items"], [
                {"type": "message", "role": "user", "content": [{"type": "input_text", "text": "latest"}]},
                compacted,
            ])
            self.assertEqual(
                result["usage"],
                {"input": 10, "output": 5, "cacheRead": 7, "cacheWrite": 3, "totalTokens": 25},
            )
        finally:
            server.shutdown()
            server.server_close()

    def test_compact_builder_keeps_streamed_item_over_empty_terminal_output(self):
        builder = main.CodexCompactBuilder()
        compacted = {"type": "compaction", "id": "cmp-1", "encrypted_content": "blob", "future": True}
        stream = [
            "event: response.output_item.done",
            "data: " + json.dumps({"type": "response.output_item.done", "output_index": 0, "item": compacted}),
            "",
            "event: response.completed",
            "data: " + json.dumps({"type": "response.completed", "response": {"id": "resp-1", "status": "completed", "output": []}}),
            "",
        ]
        for line in stream:
            builder.feed(line)
        self.assertTrue(builder.terminal)
        self.assertEqual(builder.result()["items"], [compacted])

    def test_compact_builder_uses_terminal_output_when_nothing_streamed(self):
        builder = main.CodexCompactBuilder()
        compacted = {"type": "compaction", "id": "cmp-2", "encrypted_content": "blob"}
        for line in [
            "event: response.completed",
            "data: " + json.dumps({"type": "response.completed", "response": {"id": "resp-2", "status": "completed", "output": [compacted]}}),
            "",
        ]:
            builder.feed(line)
        self.assertEqual(builder.result()["items"], [compacted])

    def test_responses_request_advertises_codex_compaction_beta_feature(self):
        seen = {}

        class Handler(BaseHTTPRequestHandler):
            def do_POST(self):  # noqa: N802
                seen["headers"] = {key.lower(): value for key, value in self.headers.items()}
                seen["body"] = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
                self.send_response(200)
                self.send_header("Content-Type", "text/event-stream")
                self.end_headers()
                self.wfile.write(b'data: {"type":"response.completed","response":{"status":"completed"}}\n\n')

            def log_message(self, *_args):
                return

        server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        threading.Thread(target=server.serve_forever, daemon=True).start()
        try:
            payload = {
                "model": {"id": "gpt-5.4", "provider": "openai-codex", "api": "openai-codex-responses", "baseUrl": f"http://127.0.0.1:{server.server_port}", "input": ["text"]},
                "credential": {"type": "oauth", "value": {"access": "access", "accountId": "acct"}},
                "request": {"sessionId": "session-1", "messages": [{"role": "user", "content": [{"type": "text", "text": "hi"}]}]},
            }
            main.stream_codex(threading.Event(), payload, lambda _event: None, "stream-1")
            self.assertEqual(seen["headers"]["x-codex-beta-features"], "remote_compaction_v2")
            self.assertEqual(seen["headers"]["originator"], "codex_cli_rs")
            self.assertTrue(seen["headers"]["user-agent"].startswith("codex_cli_rs/"))
            self.assertEqual(seen["headers"]["session-id"], "session-1")
            self.assertEqual(seen["headers"]["thread-id"], "session-1")
            # Ordinary turns carry turn metadata, not a compaction descriptor.
            metadata = json.loads(seen["headers"]["x-codex-turn-metadata"])
            self.assertEqual(metadata["request_kind"], "turn")
            self.assertNotIn("compaction", metadata)
            # The window id is mirrored between header and body client_metadata.
            self.assertEqual(metadata["window_id"], seen["headers"]["x-codex-window-id"])
            client_metadata = seen["body"]["client_metadata"]
            self.assertEqual(client_metadata["x-codex-window-id"], metadata["window_id"])
            self.assertEqual(client_metadata["session_id"], "session-1")
            self.assertEqual(client_metadata["turn_id"], metadata["turn_id"])
            self.assertTrue(client_metadata["x-codex-installation-id"])
        finally:
            server.shutdown()
            server.server_close()

    def test_remote_compaction_v2_rejects_incomplete_and_missing_output(self):
        builder = main.CodexCompactBuilder()
        with self.assertRaisesRegex(RuntimeError, "incomplete: max_output_tokens"):
            builder.process("", {
                "type": "response.incomplete",
                "response": {
                    "status": "incomplete",
                    "incomplete_details": {"reason": "max_output_tokens"},
                },
            })

        builder = main.CodexCompactBuilder()
        builder.process("", {"type": "response.completed", "response": {"status": "completed"}})
        with self.assertRaisesRegex(RuntimeError, "exactly one"):
            builder.result()

    def test_remote_compaction_v2_retains_recent_user_turns_only(self):
        items = [
            {"type": "message", "role": "user", "content": [{"type": "input_text", "text": "old"}]},
            {"type": "function_call", "call_id": "call-1"},
            {"type": "message", "role": "assistant", "content": [{"type": "output_text", "text": "reply"}]},
            {"type": "message", "role": "user", "content": [{"type": "input_text", "text": "latest"}]},
        ]
        retained = main.retained_compaction_items(items)
        self.assertEqual([item["content"][0]["text"] for item in retained], ["old", "latest"])
        self.assertIsNot(retained[0], items[0])

    def test_remote_compaction_v2_honors_pre_request_cancellation(self):
        cancelled = threading.Event()
        cancelled.set()
        with self.assertRaisesRegex(RuntimeError, "cancelled"):
            main.compact_codex(cancelled, {})

    def test_cancellation_signal_closes_only_bound_response(self):
        class Response:
            def __init__(self):
                self.closed = False

            def close(self):
                self.closed = True

        signal = main.CancellationSignal()
        response = Response()
        signal.bind(response)
        signal.set()
        self.assertTrue(signal.is_set())
        self.assertTrue(response.closed)
        signal.unbind(response)

    def test_provider_compact_rpc_returns_result_and_preserves_refresh_shape(self):
        sidecar = main.Sidecar()
        sent = []
        sidecar.send = sent.append
        expected = {
            "items": [{"type": "compaction", "encrypted_content": "opaque"}],
            "usage": {"input": 1, "output": 2, "totalTokens": 3},
        }
        with mock.patch.object(main, "compact_codex", return_value=expected):
            sidecar.handle({
                "jsonrpc": "2.0",
                "id": "compact-1",
                "method": "provider.compact",
                "params": {"provider": "openai-codex"},
            })
            deadline = time.monotonic() + 2
            while not sent and time.monotonic() < deadline:
                time.sleep(0.01)
        self.assertEqual(sent, [{"jsonrpc": "2.0", "id": "compact-1", "result": expected}])

        sent.clear()
        future_credential = {
            "type": "oauth",
            "value": {
                "access": "access",
                "refresh": "refresh",
                "expires": main.now_ms() + main.REFRESH_WINDOW + 60_000,
                "accountId": "acct",
            },
        }
        sidecar.handle({
            "jsonrpc": "2.0",
            "id": "refresh-1",
            "method": "provider.auth.refresh",
            "params": {"credential": future_credential},
        })
        self.assertEqual(sent, [{"jsonrpc": "2.0", "id": "refresh-1", "result": {}}])

        sent.clear()
        expired_credential = {
            "type": "oauth",
            "value": {
                "access": "old-access",
                "refresh": "old-refresh",
                "expires": main.now_ms() - 1,
                "accountId": "acct",
            },
        }
        token = {
            "access_token": jwt_for_account("acct"),
            "refresh_token": "new-refresh",
            "expires_in": 3600,
        }
        with mock.patch.object(main, "form_json", return_value=token):
            sidecar.handle({
                "jsonrpc": "2.0",
                "id": "refresh-2",
                "method": "provider.auth.refresh",
                "params": {"credential": expired_credential},
            })
        refreshed = sent[0]["result"]
        self.assertTrue(refreshed["refreshed"])
        self.assertEqual(refreshed["credential"]["type"], "oauth")
        self.assertEqual(refreshed["credential"]["value"]["refresh"], "new-refresh")
        self.assertEqual(refreshed["credential"]["value"]["accountId"], "acct")

    def test_provider_compact_rpc_preserves_http_status_and_body(self):
        sidecar = main.Sidecar()
        sent = []
        sidecar.send = sent.append
        upstream = urllib.error.HTTPError(
            "https://example.test/codex/responses",
            422,
            "Unprocessable Entity",
            {},
            io.BytesIO(b'{"error":{"message":"compaction unavailable"}}'),
        )
        with mock.patch.object(main, "compact_codex", side_effect=upstream):
            sidecar.handle({
                "jsonrpc": "2.0",
                "id": "compact-error",
                "method": "provider.compact",
                "params": {"provider": "openai-codex"},
            })
            deadline = time.monotonic() + 2
            while not sent and time.monotonic() < deadline:
                time.sleep(0.01)
        self.assertEqual(sent[0]["error"]["code"], 422)
        self.assertIn("compaction unavailable", sent[0]["error"]["message"])

    def test_manifest_advertises_codex_v2_compaction(self):
        provider = main.provider_spec()
        self.assertTrue(provider["models"])
        self.assertTrue(
            all(
                model.get("compaction") == {"standalone": "codex-v2"}
                for model in provider["models"]
            )
        )

    def stream_events(self, stream):
        """Run one Codex SSE stream against a fake upstream and return events."""

        class Handler(BaseHTTPRequestHandler):
            def do_POST(self):  # noqa: N802
                self.send_response(200)
                self.send_header("Content-Type", "text/event-stream")
                self.end_headers()
                for event in stream:
                    self.wfile.write(("data: " + json.dumps(event) + "\n\n").encode())

            def log_message(self, *_args):
                return

        server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        threading.Thread(target=server.serve_forever, daemon=True).start()
        try:
            events = []
            payload = {
                "model": {"id": "gpt-5.4", "provider": "openai-codex", "api": "openai-codex-responses", "baseUrl": f"http://127.0.0.1:{server.server_port}", "input": ["text"]},
                "credential": {"type": "oauth", "value": {"access": "access", "accountId": "acct"}},
                "request": {"messages": [{"role": "user", "content": [{"type": "text", "text": "run"}]}]},
            }
            main.stream_codex(threading.Event(), payload, events.append, "stream-test")
            return events
        finally:
            server.shutdown()
            server.server_close()

    def test_stream_separates_reasoning_and_tool_call_with_shared_output_index(self):
        # Gateway quirk: a reasoning item and a function call arrive with the
        # same output_index. Correlating by output index merged them into one
        # slot and replayed a reasoning item carrying name/arguments.
        events = self.stream_events([
            {"type": "response.created", "response": {"id": "resp-1"}},
            {"type": "response.output_item.added", "output_index": 0, "item": {"type": "reasoning", "id": "rs-1", "encrypted_content": "opaque", "summary": [{"type": "summary_text", "text": "thinking"}]}},
            {"type": "response.output_item.added", "output_index": 0, "item": {"type": "function_call", "id": "fc-1", "call_id": "call-1", "name": "Grep", "arguments": '{"pattern":"x"}'}},
            {"type": "response.output_item.done", "output_index": 0, "item": {"type": "function_call", "id": "fc-1", "call_id": "call-1", "name": "Grep", "arguments": '{"pattern":"x"}'}},
            {"type": "response.completed", "response": {"id": "resp-1", "status": "completed"}},
        ])
        message = events[-1]["params"]["message"]
        kinds = [item["type"] for item in message["content"]]
        self.assertEqual(kinds, ["thinking", "toolCall"])
        reasoning, call = message["content"]
        self.assertEqual(reasoning["thinking"], "thinking")
        signature = json.loads(reasoning["thinkingSignature"])
        self.assertEqual(signature["id"], "rs-1")
        self.assertNotIn("name", signature)
        self.assertNotIn("arguments", signature)
        self.assertNotIn("name", reasoning)
        self.assertEqual(call["name"], "Grep")
        self.assertEqual(call["arguments"], {"pattern": "x"})
        self.assertEqual(message["stopReason"], "toolUse")

    def test_stream_routes_item_less_delta_through_output_index(self):
        events = self.stream_events([
            {"type": "response.output_item.added", "output_index": 0, "item": {"type": "function_call", "id": "fc-1", "call_id": "call-1", "name": "Grep", "arguments": ""}},
            # Some gateways omit item_id on argument deltas; the output index
            # must still reach the item the added event registered.
            {"type": "response.function_call_arguments.delta", "output_index": 0, "call_id": "call-1", "delta": '{"pattern":"y"}'},
            {"type": "response.function_call_arguments.done", "output_index": 0, "call_id": "call-1", "arguments": '{"pattern":"y"}'},
            {"type": "response.completed", "response": {"id": "resp-1", "status": "completed"}},
        ])
        final = events[-1]["params"]["message"]
        self.assertEqual([item["type"] for item in final["content"]], ["toolCall"])
        self.assertEqual(final["content"][0]["arguments"], {"pattern": "y"})
        self.assertEqual(final["content"][0]["itemId"], "fc-1")

    def test_build_request_strips_tool_fields_from_reasoning(self):
        signature = json.dumps({"type": "reasoning", "id": "rs-1", "encrypted_content": "opaque", "name": "Grep", "arguments": {"pattern": "x"}})
        payload = {
            "model": {"id": "gpt-5.6", "input": ["text"]},
            "request": {
                "messages": [
                    {"role": "user", "content": [{"type": "text", "text": "run"}]},
                    {"role": "assistant", "content": [
                        {"type": "thinking", "thinking": "summary", "thinkingSignature": signature},
                        {"type": "toolCall", "id": "call-1", "name": "Grep", "arguments": {"pattern": "x"}},
                    ]},
                    {"role": "toolResult", "toolCallId": "call-1", "content": [{"type": "text", "text": "ok"}]},
                ],
            },
        }
        body = main.build_request(payload)
        reasoning = body["input"][1]
        self.assertEqual(reasoning["type"], "reasoning")
        self.assertEqual(reasoning["encrypted_content"], "opaque")
        self.assertNotIn("name", reasoning)
        self.assertNotIn("arguments", reasoning)

    def test_stream_failed_event_preserves_response_message(self):
        class Handler(BaseHTTPRequestHandler):
            def do_POST(self):  # noqa: N802
                self.send_response(200)
                self.send_header("Content-Type", "text/event-stream")
                self.end_headers()
                event = {"type": "response.failed", "response": {"id": "resp-failed", "status": "failed", "error": {"message": "quota exceeded"}}}
                self.wfile.write(("data: " + json.dumps(event) + "\n\n").encode())

            def log_message(self, *_args):
                return

        server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        threading.Thread(target=server.serve_forever, daemon=True).start()
        try:
            events = []
            payload = {
                "model": {"id": "gpt-5.4", "provider": "openai-codex", "api": "openai-codex-responses", "baseUrl": f"http://127.0.0.1:{server.server_port}", "input": ["text"]},
                "credential": {"type": "oauth", "value": {"access": "access", "accountId": "acct"}},
                "request": {"messages": [{"role": "user", "content": [{"type": "text", "text": "hi"}]}]},
            }
            main.stream_codex(threading.Event(), payload, events.append, "stream-failed")
            self.assertEqual([event["params"]["type"] for event in events], ["start", "error"])
            failed = events[-1]["params"]
            self.assertEqual(failed["error"], "quota exceeded")
            self.assertEqual(failed["message"]["stopReason"], "error")
            self.assertEqual(failed["message"]["responseId"], "resp-failed")
        finally:
            server.shutdown()
            server.server_close()

    def test_stream_function_call_and_response_done(self):
        class Handler(BaseHTTPRequestHandler):
            def do_POST(self):  # noqa: N802
                self.send_response(200)
                self.send_header("Content-Type", "text/event-stream")
                self.end_headers()
                events = [
                    {"type": "response.created", "response": {"id": "resp-2"}},
                    {"type": "response.output_item.added", "output_index": 0, "item": {"type": "function_call", "id": "fc-1", "call_id": "call-1", "name": "run", "arguments": ""}},
                    # Official Responses argument events identify the item;
                    # call_id is carried by the output item itself.
                    {"type": "response.function_call_arguments.delta", "output_index": 0, "item_id": "fc-1", "delta": '{"x":'},
                    {"type": "response.function_call_arguments.done", "output_index": 0, "item_id": "fc-1", "arguments": '{"x":1}'},
                    {"type": "response.output_item.done", "output_index": 0, "item": {"type": "function_call", "id": "fc-1", "call_id": "call-1", "name": "run", "arguments": '{"x":1}'}},
                    {"type": "response.output_item.added", "output_index": 1, "item": {"type": "function_call", "id": "fc-2", "call_id": "call-2", "name": "run", "arguments": ""}},
                    {"type": "response.function_call_arguments.delta", "output_index": 1, "item_id": "fc-2", "call_id": "call-2", "delta": '{"y":2}'},
                    {"type": "response.function_call_arguments.done", "output_index": 1, "item_id": "fc-2", "call_id": "call-2", "arguments": '{"y":2}'},
                    {"type": "response.output_item.done", "output_index": 1, "item": {"type": "function_call", "id": "fc-2", "call_id": "call-2", "name": "run", "arguments": '{"y":2}'}},
                    {"type": "response.done", "response": {"id": "resp-2", "status": "completed", "usage": {"input_tokens": 10, "output_tokens": 4, "total_tokens": 17, "input_tokens_details": {"cached_tokens": 2, "cache_write_tokens": 3}}}},
                ]
                for event in events:
                    self.wfile.write(("data: " + json.dumps(event) + "\n\n").encode())

            def log_message(self, *_args):
                return

        server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        threading.Thread(target=server.serve_forever, daemon=True).start()
        try:
            events = []
            payload = {
                "model": {"id": "gpt-5.4", "provider": "openai-codex", "api": "openai-codex-responses", "baseUrl": f"http://127.0.0.1:{server.server_port}", "input": ["text"]},
                "credential": {"type": "oauth", "value": {"access": "access", "accountId": "acct"}},
                "request": {"messages": [{"role": "user", "content": [{"type": "text", "text": "run"}]}]},
            }
            main.stream_codex(threading.Event(), payload, events.append, "stream-2")
            types = [event["params"]["type"] for event in events]
            self.assertEqual(types, ["start", "toolcall_start", "toolcall_delta", "toolcall_end", "toolcall_start", "toolcall_delta", "toolcall_end", "done"])
            self.assertTrue(all("contentIndex" in event["params"] for event in events[1:-1]))
            self.assertEqual(events[1]["params"]["toolCallId"], "call-1")
            self.assertNotIn("toolCallID", events[1]["params"])
            final = events[-1]["params"]["message"]
            self.assertEqual(final["content"][0]["id"], "call-1")
            self.assertEqual(final["content"][0]["itemId"], "fc-1")
            self.assertEqual(final["content"][0]["arguments"], {"x": 1})
            self.assertEqual(final["content"][1]["id"], "call-2")
            self.assertEqual(final["content"][1]["itemId"], "fc-2")
            self.assertEqual(events[5]["params"]["contentIndex"], 1)
            self.assertEqual(final["usage"]["input"], 5)
            self.assertEqual(final["usage"]["cacheWrite"], 3)
        finally:
            server.shutdown()
            server.server_close()

    def test_stalled_response_headers_fail_within_header_budget(self):
        # A connection the upstream accepts but never answers used to hold the
        # turn for the full stream timeout (5 minutes); Ki can only retry after
        # the sidecar errors, so the header budget must fail fast.
        class Handler(BaseHTTPRequestHandler):
            def do_POST(self):  # noqa: N802
                time.sleep(2)
                self.send_response(200)
                self.send_header("Content-Type", "text/event-stream")
                self.end_headers()

            def log_message(self, *_args):
                return

        server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        threading.Thread(target=server.serve_forever, daemon=True).start()
        original = main.STREAM_HEADER_TIMEOUT
        main.STREAM_HEADER_TIMEOUT = 0.3
        try:
            payload = {
                "model": {"id": "gpt-5.4", "baseUrl": f"http://127.0.0.1:{server.server_port}"},
                "credential": {"value": {"access": "access", "accountId": "acct"}},
                "request": {"messages": [{"role": "user", "content": [{"type": "text", "text": "hi"}]}]},
            }
            started = time.monotonic()
            with self.assertRaises(TimeoutError):
                main.stream_codex(threading.Event(), payload, lambda _event: None, "stream-stall")
            self.assertLess(time.monotonic() - started, 2)
        finally:
            main.STREAM_HEADER_TIMEOUT = original
            server.shutdown()
            server.server_close()

    def test_idle_stream_survives_past_header_budget(self):
        # Once headers arrive the socket must use the longer idle budget so a
        # reasoning model that pauses between events is not cut off.
        class Handler(BaseHTTPRequestHandler):
            def do_POST(self):  # noqa: N802
                self.send_response(200)
                self.send_header("Content-Type", "text/event-stream")
                self.end_headers()
                self.wfile.flush()
                time.sleep(1.0)
                self.wfile.write(b'data: {"type":"response.created","response":{"id":"resp-idle"}}\n\n')
                self.wfile.write(b'data: {"type":"response.output_text.delta","item_id":"msg-1","delta":"hi"}\n\n')
                self.wfile.write(b'data: {"type":"response.completed","response":{"status":"completed"}}\n\n')

            def log_message(self, *_args):
                return

        server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        threading.Thread(target=server.serve_forever, daemon=True).start()
        original_header = main.STREAM_HEADER_TIMEOUT
        original_idle = main.STREAM_IDLE_TIMEOUT
        main.STREAM_HEADER_TIMEOUT = 0.3
        main.STREAM_IDLE_TIMEOUT = 5
        try:
            events = []
            payload = {
                "model": {"id": "gpt-5.4", "provider": "openai-codex", "api": "openai-codex-responses", "baseUrl": f"http://127.0.0.1:{server.server_port}", "input": ["text"]},
                "credential": {"type": "oauth", "value": {"access": "access", "accountId": "acct"}},
                "request": {"messages": [{"role": "user", "content": [{"type": "text", "text": "hi"}]}]},
            }
            main.stream_codex(threading.Event(), payload, events.append, "stream-idle")
            self.assertIn("done", [event["params"]["type"] for event in events])
            self.assertEqual(events[-1]["params"]["message"]["responseId"], "resp-idle")
        finally:
            main.STREAM_HEADER_TIMEOUT = original_header
            main.STREAM_IDLE_TIMEOUT = original_idle
            server.shutdown()
            server.server_close()


if __name__ == "__main__":
    unittest.main()
