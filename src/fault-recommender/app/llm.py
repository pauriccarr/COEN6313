import json
import os

import httpx

OLLAMA_URL = os.environ.get("OLLAMA_SERVICE_URL", "http://ollama:11434").rstrip("/")
OLLAMA_MODEL = os.environ.get("OLLAMA_MODEL", "llama3")
LLM_TIMEOUT = float(os.environ.get("LLM_TIMEOUT", "240"))


def chat_json(system: str, user: str) -> tuple[dict, dict]:
    """One Ollama chat call in JSON mode. Returns (parsed JSON, usage)."""
    r = httpx.post(f"{OLLAMA_URL}/api/chat", timeout=httpx.Timeout(LLM_TIMEOUT, connect=5.0), json={
        "model": OLLAMA_MODEL,
        "stream": False,
        "format": "json",
        "options": {"temperature": 0.2},
        "messages": [{"role": "system", "content": system}, {"role": "user", "content": user}],
    })
    r.raise_for_status()
    body = r.json()
    usage = {"input_tokens": body.get("prompt_eval_count", 0), "output_tokens": body.get("eval_count", 0),
             "model": body.get("model", OLLAMA_MODEL)}
    return json.loads(body["message"]["content"]), usage


def available() -> bool:
    try:
        return httpx.get(f"{OLLAMA_URL}/api/tags", timeout=3.0).status_code == 200
    except httpx.HTTPError:
        return False
