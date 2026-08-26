"""OpenAI Codex OAuth provider extension for Ki.

The process speaks the Ki provider NDJSON RPC protocol on stdin/stdout.  It
uses only the Python standard library so uv can run it without a compiled
sidecar or a platform-specific virtualenv.
"""

from __future__ import annotations

import base64
import copy
import hashlib
import http.server
import json
import os
import queue
import secrets
import sys
import threading
import time
import urllib.error
import urllib.parse
import urllib.request
from typing import Any, Callable


CLIENT_ID = "app_EMoamEEZ73f0CkXaXp7hrann"
AUTH_BASE = "https://auth.openai.com"
CALLBACK_PATH = "/auth/callback"
CALLBACK_PORT = 1455
REFRESH_WINDOW = 60_000
DEVICE_LIFETIME = 15 * 60


def now_ms() -> int:
    return int(time.time() * 1000)


def auth_base() -> str:
    return os.environ.get("KI_CODEX_AUTH_BASE_URL", AUTH_BASE).rstrip("/")


def callback_port() -> int:
    try:
        port = int(os.environ.get("KI_CODEX_CALLBACK_PORT", CALLBACK_PORT))
        return port if 0 < port < 65536 else CALLBACK_PORT
    except ValueError:
        return CALLBACK_PORT


def redirect_uri() -> str:
    return f"http://localhost:{callback_port()}{CALLBACK_PATH}"


def safe_error(exc: BaseException) -> str:
    return str(exc)[:500] or "provider authentication failed"


def http_json(method: str, url: str, body: Any, timeout: float = 30) -> tuple[int, Any]:
    data = json.dumps(body).encode()
    request = urllib.request.Request(url, data=data, method=method, headers={"Content-Type": "application/json"})
    try:
        with urllib.request.urlopen(request, timeout=timeout) as response:
            raw = response.read()
            return response.status, json.loads(raw or b"{}")
    except urllib.error.HTTPError as exc:
        raw = exc.read()
        try:
            payload = json.loads(raw or b"{}")
        except json.JSONDecodeError:
            payload = {}
        return exc.code, payload


def form_json(url: str, values: dict[str, str], timeout: float = 30) -> Any:
    request = urllib.request.Request(
        url,
        data=urllib.parse.urlencode(values).encode(),
        method="POST",
        headers={"Content-Type": "application/x-www-form-urlencoded"},
    )
    try:
        with urllib.request.urlopen(request, timeout=timeout) as response:
            payload = json.loads(response.read() or b"{}")
            if response.status < 200 or response.status >= 300:
                raise RuntimeError(f"token request failed ({response.status})")
            return payload
    except urllib.error.HTTPError as exc:
        raise RuntimeError(f"token request failed ({exc.code})") from exc


def pkce() -> tuple[str, str]:
    verifier = base64.urlsafe_b64encode(secrets.token_bytes(32)).rstrip(b"=").decode()
    challenge = base64.urlsafe_b64encode(hashlib.sha256(verifier.encode()).digest()).rstrip(b"=").decode()
    return verifier, challenge


def authorization_flow() -> tuple[str, str, str]:
    verifier, challenge = pkce()
    state = secrets.token_hex(16)
    query = urllib.parse.urlencode(
        {
            "response_type": "code",
            "client_id": CLIENT_ID,
            "redirect_uri": redirect_uri(),
            "scope": "openid profile email offline_access",
            "code_challenge": challenge,
            "code_challenge_method": "S256",
            "state": state,
            "id_token_add_organizations": "true",
            "codex_cli_simplified_flow": "true",
            "originator": "pi",
        }
    )
    return verifier, state, f"{auth_base()}/oauth/authorize?{query}"


def parse_auth_input(value: str) -> tuple[str, str]:
    value = value.strip()
    if not value:
        return "", ""
    parsed = urllib.parse.urlparse(value)
    if parsed.scheme and parsed.netloc:
        query = urllib.parse.parse_qs(parsed.query)
        return query.get("code", [""])[0].strip(), query.get("state", [""])[0].strip()
    if "#" in value:
        return tuple(part.strip() for part in value.split("#", 1))  # type: ignore[return-value]
    if "code=" in value:
        query = urllib.parse.parse_qs(value)
        return query.get("code", [""])[0].strip(), query.get("state", [""])[0].strip()
    return value, ""


def account_id(access: str) -> str:
    parts = access.split(".")
    if len(parts) != 3:
        return ""
    try:
        padding = "=" * (-len(parts[1]) % 4)
        payload = json.loads(base64.urlsafe_b64decode(parts[1] + padding))
        return str(payload.get("https://api.openai.com/auth", {}).get("chatgpt_account_id", ""))
    except (ValueError, TypeError, json.JSONDecodeError):
        return ""


def credential_from_token(token: dict[str, Any]) -> dict[str, Any]:
    access = str(token.get("access_token", ""))
    refresh = str(token.get("refresh_token", ""))
    expires = int(token.get("expires_in", 0) or 0)
    if not access or not refresh or expires <= 0:
        raise RuntimeError("token response is incomplete")
    acct = account_id(access)
    if not acct:
        raise RuntimeError("access token does not contain a ChatGPT account id")
    return {"type": "oauth", "value": {"access": access, "refresh": refresh, "expires": now_ms() + expires * 1000, "accountId": acct}}


def refresh_credential(credential: dict[str, Any]) -> tuple[dict[str, Any], bool]:
    value = credential.get("value") or {}
    expires = int(value.get("expires", 0) or 0)
    if expires > now_ms() + REFRESH_WINDOW:
        return credential, False
    refresh = str(value.get("refresh", ""))
    if not refresh:
        raise RuntimeError("OAuth credential has no refresh token")
    token = form_json(
        f"{auth_base()}/oauth/token",
        {"grant_type": "refresh_token", "refresh_token": refresh, "client_id": CLIENT_ID},
    )
    return credential_from_token(token), True


class OAuthSession:
    def __init__(self, request: dict[str, Any], send_event: Callable[[dict[str, Any]], None]) -> None:
        self.request = request
        self.send_event = send_event
        self.manual: queue.Queue[str] = queue.Queue(maxsize=1)
        self.cancelled = threading.Event()
        self.callback_server: http.server.ThreadingHTTPServer | None = None

    def event(self, kind: str, **values: Any) -> None:
        payload = {"requestId": self.request.get("requestId", ""), "provider": "openai-codex", "type": kind}
        payload.update({key: value for key, value in values.items() if value is not None and value != ""})
        self.send_event({"jsonrpc": "2.0", "method": "provider.auth.event", "params": payload})

    def close(self) -> None:
        self.cancelled.set()
        if self.callback_server:
            self.callback_server.shutdown()
            self.callback_server.server_close()

    def run(self) -> None:
        try:
            if self.request.get("mode", "browser") == "device_code":
                self.run_device()
            else:
                self.run_browser()
        except Exception as exc:
            if not self.cancelled.is_set():
                self.event("error", error=safe_error(exc))
        finally:
            self.close()

    def run_browser(self) -> None:
        verifier, state, url = authorization_flow()
        result: queue.Queue[str] = queue.Queue(maxsize=1)

        class Callback(http.server.BaseHTTPRequestHandler):
            def do_GET(inner_self: Any) -> None:  # noqa: N802
                parsed = urllib.parse.urlparse(inner_self.path)
                query = urllib.parse.parse_qs(parsed.query)
                if parsed.path != CALLBACK_PATH or query.get("state", [""])[0] != state:
                    inner_self.send_response(400)
                    inner_self.end_headers()
                    return
                code = query.get("code", [""])[0]
                if code:
                    try:
                        result.put_nowait(code)
                    except queue.Full:
                        pass
                inner_self.send_response(200 if code else 400)
                inner_self.send_header("Content-Type", "text/html; charset=utf-8")
                inner_self.end_headers()
                inner_self.wfile.write(b"<p>OpenAI authentication completed. You can close this window.</p>")

            def log_message(inner_self: Any, *_args: Any) -> None:
                return

        try:
            self.callback_server = http.server.ThreadingHTTPServer(("127.0.0.1", callback_port()), Callback)
        except OSError as exc:
            raise RuntimeError(f"OAuth callback port {callback_port()} is unavailable") from exc
        threading.Thread(target=self.callback_server.serve_forever, daemon=True).start()
        self.event("auth_url", url=url, instructions="Complete login in a browser. If the callback cannot reach this machine, paste the redirect URL or authorization code.")
        code = ""
        while not self.cancelled.is_set() and not code:
            try:
                code = result.get(timeout=0.2)
            except queue.Empty:
                try:
                    code, supplied_state = parse_auth_input(self.manual.get_nowait())
                    if supplied_state and supplied_state != state:
                        raise RuntimeError("OAuth state mismatch")
                except queue.Empty:
                    pass
        if self.cancelled.is_set():
            return
        token = form_json(f"{auth_base()}/oauth/token", {"grant_type": "authorization_code", "client_id": CLIENT_ID, "code": code, "code_verifier": verifier, "redirect_uri": redirect_uri()})
        self.event("completed", credential=credential_from_token(token))

    def run_device(self) -> None:
        status, body = http_json("POST", f"{auth_base()}/api/accounts/deviceauth/usercode", {"client_id": CLIENT_ID})
        if status != 200:
            raise RuntimeError(f"device code request failed ({status})")
        device_id, user_code = body.get("device_auth_id", ""), body.get("user_code", "")
        interval = int(float(body.get("interval", 5) or 5))
        if not device_id or not user_code:
            raise RuntimeError("invalid device code response")
        self.event("device_code", userCode=user_code, verificationUri=f"{auth_base()}/codex/device", intervalSeconds=interval, expiresInSeconds=DEVICE_LIFETIME)
        deadline = time.monotonic() + DEVICE_LIFETIME
        while not self.cancelled.is_set() and time.monotonic() < deadline:
            status, body = http_json("POST", f"{auth_base()}/api/accounts/deviceauth/token", {"device_auth_id": device_id, "user_code": user_code})
            if status == 200:
                code, verifier = body.get("authorization_code", ""), body.get("code_verifier", "")
                if not code or not verifier:
                    raise RuntimeError("invalid device auth response")
                token = form_json(f"{auth_base()}/oauth/token", {"grant_type": "authorization_code", "client_id": CLIENT_ID, "code": code, "code_verifier": verifier, "redirect_uri": f"{auth_base()}/deviceauth/callback"})
                self.event("completed", credential=credential_from_token(token))
                return
            error = body.get("error", {})
            error_code = error if isinstance(error, str) else error.get("code", "") if isinstance(error, dict) else ""
            if status not in (403, 404) and error_code not in ("deviceauth_authorization_pending", "authorization_pending", "slow_down"):
                raise RuntimeError(f"device auth failed ({status})")
            if error_code == "slow_down":
                interval += 5
            self.cancelled.wait(interval)
        if not self.cancelled.is_set():
            raise RuntimeError("device code expired")


def text_of(message: dict[str, Any]) -> str:
    return "".join(str(item.get("text", "")) for item in message.get("content", []) if item.get("type", "text") in ("", "text"))


def tool_calls(message: dict[str, Any]) -> list[dict[str, Any]]:
    return [item for item in message.get("content", []) if item.get("type") == "toolCall"]


def replayable(messages: list[dict[str, Any]]) -> list[dict[str, Any]]:
    out: list[dict[str, Any]] = []
    for index, message in enumerate(messages):
        if message.get("role") == "assistant" and (message.get("stopReason") in ("aborted", "error") or (not text_of(message) and not message.get("content") and not tool_calls(message))):
            continue
        if message.get("role") == "toolResult" and not out:
            continue
        out.append(message)
        if message.get("role") == "assistant":
            calls = tool_calls(message)
            next_is_result = index + 1 < len(messages) and messages[index + 1].get("role") == "toolResult"
            if calls and not next_is_result:
                for call in calls:
                    out.append({"role": "toolResult", "toolCallID": call.get("id", ""), "toolType": call.get("toolType", "function"), "content": [{"type": "text", "text": "No result provided"}], "isError": True})
    return out


def image_item(item: dict[str, Any]) -> dict[str, Any]:
    data = item.get("data", "")
    mime = item.get("mimeType", "image/png")
    url = data if str(data).startswith(("http://", "https://", "data:")) else f"data:{mime};base64,{data}"
    return {"type": "input_image", "image_url": url, "detail": "auto"}


def input_items(message: dict[str, Any], model: dict[str, Any]) -> list[dict[str, Any]]:
    role = message.get("role")
    allow_image = "image" in model.get("input", [])
    if role == "toolResult":
        output: Any = []
        for item in message.get("content", []):
            if item.get("type") in ("text", ""):
                output.append({"type": "input_text", "text": item.get("text", "")})
            elif item.get("type") == "image" and allow_image:
                output.append(image_item(item))
        if len(output) == 1 and output[0].get("type") == "input_text":
            output = output[0]["text"]
        kind = "custom_tool_call_output" if message.get("toolType") == "custom" else "function_call_output"
        return [{"type": kind, "call_id": message.get("toolCallID", ""), "output": output}]
    if role == "assistant":
        result: list[dict[str, Any]] = []
        for item in message.get("content", []):
            if item.get("type") == "thinking" and item.get("thinkingSignature"):
                try:
                    result.append(json.loads(item["thinkingSignature"]))
                except (TypeError, json.JSONDecodeError):
                    pass
        if text_of(message):
            item = {"type": "message", "role": "assistant", "status": "completed", "content": [{"type": "output_text", "text": text_of(message), "annotations": []}]}
            signature = next((x.get("textSignature") for x in message.get("content", []) if x.get("type") == "text" and x.get("textSignature")), "")
            if signature:
                try:
                    meta = json.loads(signature)
                    item.update({key: meta[key] for key in ("id", "phase") if key in meta})
                except (TypeError, json.JSONDecodeError):
                    item["id"] = signature
            result.append(item)
        for call in tool_calls(message):
            if call.get("toolType") == "custom":
                item = {"type": "custom_tool_call", "call_id": call.get("id", ""), "name": call.get("name", ""), "input": call.get("input", "")}
            else:
                arguments = call.get("argumentsRaw") or json.dumps(call.get("arguments", {}), separators=(",", ":"))
                item = {"type": "function_call", "call_id": call.get("id", ""), "name": call.get("name", ""), "arguments": arguments}
            if call.get("itemId"):
                item["id"] = call["itemId"]
            result.append(item)
        return result
    content: list[dict[str, Any]] = []
    for item in message.get("content", []):
        if item.get("type") in ("text", ""):
            content.append({"type": "input_text", "text": item.get("text", "")})
        elif item.get("type") == "image" and allow_image:
            content.append(image_item(item))
    return [{"type": "message", "role": "user", "content": content}]


def build_request(payload: dict[str, Any]) -> dict[str, Any]:
    request = payload.get("request", {})
    model = payload.get("model", {})
    body: dict[str, Any] = {
        "model": model.get("id", ""),
        "store": False,
        "stream": True,
        "instructions": request.get("system") or "You are a helpful assistant.",
        "input": [item for message in replayable(request.get("messages", [])) for item in input_items(message, model)],
        "text": {"verbosity": "low"},
        "include": ["reasoning.encrypted_content"],
        "tool_choice": "auto",
        "parallel_tool_calls": True,
    }
    session_id = request.get("sessionId", "")
    if session_id:
        body["prompt_cache_key"] = session_id[:64]
    if request.get("maxTokens"):
        body["max_output_tokens"] = request["maxTokens"]
    effort = request.get("thinkingEffort", "")
    if effort and effort != "off":
        mapped = (request.get("thinkingLevelMap") or {}).get(effort, effort)
        if mapped:
            body["reasoning"] = {"effort": mapped, "summary": "auto"}
    if request.get("tools"):
        body["tools"] = []
        for tool in request["tools"]:
            if tool.get("type") == "custom":
                entry = {"type": "custom", "name": tool.get("name", ""), "description": tool.get("description", "")}
                if tool.get("format") is not None:
                    entry["format"] = tool["format"]
            else:
                entry = {"type": "function", "name": tool.get("name", ""), "description": tool.get("description", ""), "parameters": tool.get("parameters", {})}
            body["tools"].append(entry)
    return body


def emit_event(send: Callable[[dict[str, Any]], None], kind: str, message: dict[str, Any], **values: Any) -> None:
    event = {"jsonrpc": "2.0", "method": "provider.stream.event", "params": {"requestId": values.pop("requestId", ""), "type": kind, "message": copy.deepcopy(message)}}
    event["params"].update({key: value for key, value in values.items() if value is not None and value != ""})
    send(event)


def response_id(obj: dict[str, Any]) -> str:
    response = obj.get("response")
    return str((response if isinstance(response, dict) else obj).get("id", ""))


def stream_codex(cancelled: threading.Event, payload: dict[str, Any], send: Callable[[dict[str, Any]], None], request_id: str) -> None:
    credential = (payload.get("credential") or {}).get("value") or {}
    if not credential.get("access") or not credential.get("accountId"):
        raise RuntimeError("invalid Codex OAuth credential")
    model = payload.get("model", {})
    base = str(model.get("baseUrl", "https://chatgpt.com/backend-api")).rstrip("/")
    url = base if base.endswith("/codex/responses") else base + ("/responses" if base.endswith("/codex") else "/codex/responses")
    raw = json.dumps(build_request(payload)).encode()
    request = urllib.request.Request(url, data=raw, method="POST", headers={"Content-Type": "application/json", "Authorization": f"Bearer {credential['access']}", "chatgpt-account-id": credential["accountId"], "OpenAI-Beta": "responses=experimental", "originator": "pi", "User-Agent": "pi (python; uv)", "Accept": "text/event-stream"})
    session_id = (payload.get("request") or {}).get("sessionId", "")
    if session_id:
        request.add_header("session-id", session_id)
        request.add_header("x-client-request-id", session_id)
    with urllib.request.urlopen(request, timeout=300) as response:
        if response.status < 200 or response.status >= 300:
            raise RuntimeError(f"Codex request failed ({response.status})")
        message = {"role": "assistant", "api": model.get("api", "openai-codex-responses"), "provider": model.get("provider", "openai-codex"), "model": model.get("id", ""), "content": []}
        item_map: dict[str, dict[str, Any]] = {}
        started_text: set[str] = set()
        ended_text: set[str] = set()
        started_thinking: set[str] = set()
        terminal = False
        emit_event(send, "start", message, requestId=request_id)
        event_name, data_lines = "", []

        def process(name: str, obj: dict[str, Any]) -> None:
            nonlocal terminal
            typ = obj.get("type") or name
            if typ in ("response.created", "response.in_progress", "response.queued"):
                message["responseId"] = response_id(obj) or message.get("responseId", "")
                return
            item = obj.get("item") if isinstance(obj.get("item"), dict) else {}
            item_id = str(obj.get("item_id") or item.get("id") or obj.get("call_id") or "")
            if typ in ("response.output_item.added", "response.output_item.done") and item:
                item_type = item.get("type", "")
                if item_type == "message":
                    existing = item_map.setdefault(item_id, {"type": "text", "text": "", "itemId": item_id})
                    if item.get("id"):
                        existing["itemId"] = item["id"]
                    for part in item.get("content", []):
                        if part.get("type") == "output_text" and part.get("text"):
                            existing["text"] = part["text"]
                elif item_type in ("function_call", "custom_tool_call"):
                    existing = item_map.setdefault(item_id, {"type": "toolCall", "id": item.get("call_id", item_id), "itemId": item.get("id", item_id), "name": item.get("name", ""), "arguments": {}, "argumentsRaw": "", "toolType": "custom" if item_type == "custom_tool_call" else "function", "input": item.get("input", "")})
                    existing.update({"name": item.get("name", existing.get("name", "")), "itemId": item.get("id", existing.get("itemId", ""))})
                elif item_type == "reasoning" and item.get("encrypted_content"):
                    signature = json.dumps(item, separators=(",", ":"))
                    message["content"].append({"type": "thinking", "thinkingSignature": signature})
                if typ == "response.output_item.done" and item_id in started_text and item_id not in ended_text:
                    ended_text.add(item_id)
                    emit_event(send, "text_end", message, requestId=request_id)
                return
            if typ == "response.output_text.delta":
                content = item_map.setdefault(item_id, {"type": "text", "text": "", "itemId": item_id})
                if item_id not in started_text:
                    started_text.add(item_id)
                    message["content"].append(content)
                    emit_event(send, "text_start", message, requestId=request_id)
                delta = str(obj.get("delta", ""))
                content["text"] += delta
                emit_event(send, "text_delta", message, requestId=request_id, delta=delta)
            elif typ in ("response.output_text.done", "response.output_item.done") and item_id in started_text:
                ended_text.add(item_id)
                emit_event(send, "text_end", message, requestId=request_id)
            elif typ in ("response.reasoning_summary_text.delta", "response.reasoning_text.delta"):
                content = item_map.setdefault(item_id, {"type": "thinking", "thinking": "", "itemId": item_id})
                if item_id not in started_thinking:
                    started_thinking.add(item_id)
                    message["content"].append(content)
                    emit_event(send, "thinking_start", message, requestId=request_id)
                delta = str(obj.get("delta", ""))
                content["thinking"] += delta
                emit_event(send, "thinking_delta", message, requestId=request_id, delta=delta)
            elif typ in ("response.reasoning_summary_text.done", "response.reasoning_text.done") and item_id in started_thinking:
                emit_event(send, "thinking_end", message, requestId=request_id)
            elif typ in ("response.function_call_arguments.delta", "response.custom_tool_call_input.delta"):
                custom = typ.startswith("response.custom")
                content = item_map.setdefault(item_id, {"type": "toolCall", "id": obj.get("call_id", item_id), "itemId": item_id, "name": obj.get("name", ""), "toolType": "custom" if custom else "function", "argumentsRaw": "", "arguments": {}, "input": ""})
                if content not in message["content"]:
                    message["content"].append(content)
                    emit_event(send, "toolcall_start", message, requestId=request_id, toolCallID=content.get("id"), toolName=content.get("name"))
                delta = str(obj.get("delta", obj.get("input", "")))
                if custom:
                    content["input"] += delta
                else:
                    content["argumentsRaw"] += delta
                emit_event(send, "toolcall_delta", message, requestId=request_id, delta=delta, toolCallID=content.get("id"), toolName=content.get("name"))
            elif typ in ("response.function_call_arguments.done", "response.custom_tool_call_input.done"):
                content = item_map.get(item_id)
                if content:
                    if obj.get("arguments") is not None:
                        content["argumentsRaw"] = obj["arguments"]
                        try:
                            content["arguments"] = json.loads(obj["arguments"])
                        except (TypeError, json.JSONDecodeError):
                            pass
                    if obj.get("input") is not None:
                        content["input"] = obj["input"]
                    emit_event(send, "toolcall_end", message, requestId=request_id, toolCallID=content.get("id"), toolName=content.get("name"))
            elif typ in ("response.completed", "response.incomplete", "response.failed", "error"):
                response = obj.get("response") if isinstance(obj.get("response"), dict) else obj
                if response_id(obj):
                    message["responseId"] = response_id(obj)
                usage = response.get("usage") if isinstance(response, dict) else None
                if usage:
                    details = usage.get("input_tokens_details") or {}
                    cached = int(details.get("cached_tokens", 0) or 0)
                    message["usage"] = {"input": max(0, int(usage.get("input_tokens", 0) or 0) - cached), "output": int(usage.get("output_tokens", 0) or 0), "cacheRead": cached, "cacheWrite": int(usage.get("cache_write_tokens", 0) or 0), "totalTokens": int(usage.get("total_tokens", 0) or 0)}
                status = response.get("status", "") if isinstance(response, dict) else ""
                if typ in ("response.failed", "error") or status in ("failed", "cancelled"):
                    message["stopReason"], message["errorMessage"] = "error", str(obj.get("message") or response.get("error") or "Codex response failed")
                elif typ == "response.incomplete" or status == "incomplete":
                    message["stopReason"] = "length"
                else:
                    message["stopReason"] = "toolUse" if tool_calls(message) else "stop"
                terminal = True

        for raw_line in response:
            if cancelled.is_set():
                return
            line = raw_line.decode(errors="replace").rstrip("\r\n")
            if not line:
                if data_lines:
                    try:
                        obj = json.loads("\n".join(data_lines))
                        process(event_name, obj)
                    except json.JSONDecodeError:
                        pass
                event_name, data_lines = "", []
                if terminal:
                    break
            elif line.startswith("event:"):
                event_name = line[6:].strip()
            elif line.startswith("data:"):
                data_lines.append(line[5:].lstrip())
        if not terminal:
            raise RuntimeError("Codex stream ended before a terminal response event")
        if message.get("stopReason") == "error":
            raise RuntimeError(message.get("errorMessage", "Codex response failed"))
        emit_event(send, "done", message, requestId=request_id)


class Sidecar:
    def __init__(self) -> None:
        self.write_lock = threading.Lock()
        self.auth: dict[str, OAuthSession] = {}
        self.streams: dict[str, threading.Event] = {}
        self.lock = threading.Lock()

    def send(self, value: dict[str, Any]) -> None:
        with self.write_lock:
            sys.stdout.write(json.dumps(value, separators=(",", ":")) + "\n")
            sys.stdout.flush()

    def reply(self, ident: Any, result: Any) -> None:
        self.send({"jsonrpc": "2.0", "id": ident, "result": result})

    def fail(self, ident: Any, message: str, code: int = -32000) -> None:
        self.send({"jsonrpc": "2.0", "id": ident, "error": {"code": code, "message": safe_error(RuntimeError(message))}})

    def auth_event(self, event: dict[str, Any]) -> None:
        self.send(event)

    def handle(self, message: dict[str, Any]) -> None:
        method, ident, params = message.get("method", ""), message.get("id"), message.get("params") or {}
        if method == "initialize":
            self.reply(ident, {"tools": [], "commands": [], "providers": [provider_spec()]})
        elif method == "provider.auth.start":
            request_id = params.get("requestId", "")
            if not request_id or params.get("provider") != "openai-codex":
                self.reply(ident, {"accepted": False})
                return
            session = OAuthSession(params, self.auth_event)
            with self.lock:
                old = self.auth.get(request_id)
                if old:
                    old.close()
                self.auth[request_id] = session
            def run_auth() -> None:
                session.run()
                with self.lock:
                    if self.auth.get(request_id) is session:
                        self.auth.pop(request_id, None)
            threading.Thread(target=run_auth, daemon=True).start()
            self.reply(ident, {"accepted": True})
        elif method == "provider.auth.input":
            session = self.auth.get(params.get("requestId", ""))
            if not session:
                self.fail(ident, "auth request not found")
                return
            try:
                session.manual.put_nowait(params.get("value", ""))
                self.reply(ident, {"accepted": True})
            except queue.Full:
                self.fail(ident, "auth input already pending")
        elif method == "provider.auth.cancel":
            session = self.auth.get(params.get("requestId", ""))
            if session:
                session.close()
        elif method == "provider.auth.refresh":
            try:
                credential, refreshed = refresh_credential(params.get("credential") or {})
                self.reply(ident, {"refreshed": refreshed, "credential": credential} if refreshed else {})
            except Exception as exc:
                self.fail(ident, safe_error(exc))
        elif method == "provider.stream.start":
            request_id = params.get("requestId", "")
            payload = params.get("request") or {}
            if not request_id or payload.get("provider") != "openai-codex":
                self.reply(ident, {"accepted": False})
                return
            cancelled = threading.Event()
            with self.lock:
                if request_id in self.streams:
                    self.reply(ident, {"accepted": False})
                    return
                self.streams[request_id] = cancelled
            def run() -> None:
                try:
                    stream_codex(cancelled, payload, self.send, request_id)
                except Exception as exc:
                    if not cancelled.is_set():
                        self.send({"jsonrpc": "2.0", "method": "provider.stream.event", "params": {"requestId": request_id, "type": "error", "error": safe_error(exc)}})
                finally:
                    self.streams.pop(request_id, None)
            threading.Thread(target=run, daemon=True).start()
            self.reply(ident, {"accepted": True})
        elif method == "provider.stream.cancel":
            event = self.streams.get(params.get("requestId", ""))
            if event:
                event.set()
        elif method == "shutdown":
            for session in list(self.auth.values()):
                session.close()
            for event in list(self.streams.values()):
                event.set()
            if ident is not None:
                self.reply(ident, {})
        elif ident is not None:
            self.reply(ident, {})


def provider_spec() -> dict[str, Any]:
    with open(os.path.join(os.path.dirname(__file__), "extension.json"), encoding="utf-8") as file:
        manifest = json.load(file)
    return manifest["providers"][0]


def main() -> None:
    sidecar = Sidecar()
    for line in sys.stdin:
        try:
            message = json.loads(line)
            if isinstance(message, dict):
                sidecar.handle(message)
        except json.JSONDecodeError:
            continue


if __name__ == "__main__":
    main()
