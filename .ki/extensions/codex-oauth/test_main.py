import base64
import json
import threading
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

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
                "thinkingEffort": "high",
                "thinkingLevelMap": {"high": "xhigh"},
                "messages": [
                    {"role": "user", "content": [{"type": "text", "text": "run"}]},
                    {"role": "assistant", "content": [{"type": "toolCall", "id": "call-1", "itemId": "fc-1", "name": "run", "arguments": {"x": 1}}]},
                ],
                "tools": [{"name": "run", "description": "run", "parameters": {"type": "object"}}],
            },
        }
        body = main.build_request(payload)
        self.assertEqual(body["prompt_cache_key"], "session-1")
        self.assertEqual(body["reasoning"]["effort"], "xhigh")
        self.assertEqual(body["input"][1]["id"], "fc-1")
        self.assertEqual(body["input"][2]["type"], "function_call_output")

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


if __name__ == "__main__":
    unittest.main()
