# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""
OllamaClient -- REST Client for Local Ollama LLM
==================================================
Communicates with a local Ollama instance for text generation,
embeddings, and model management.  Fully offline — no cloud APIs.

Default model: qwen2.5:3b (fast on CPU, ~2-5 sec/response)
Fallback: qwen2.5:7b-instruct (better quality, slower)

Usage:
    client = OllamaClient()
    response = client.generate("Explain NG Setup procedure in 5G NR")
    embedding = client.embed("5G NR NGAP NG Setup Request")
"""

import json
import time
import logging
import requests
from typing import Dict, List, Optional, Any
from dataclasses import dataclass

log = logging.getLogger("tester.ai.ollama")


@dataclass
class OllamaConfig:
    """Configuration for Ollama connection."""
    base_url: str = "http://127.0.0.1:11434"
    model: str = "qwen2.5:3b"
    fallback_model: str = "qwen2.5:7b-instruct"
    embedding_model: str = "nomic-embed-text"
    timeout: int = 120            # Generation timeout (seconds)
    embed_timeout: int = 30       # Embedding timeout (seconds)
    temperature: float = 0.3      # Lower = more focused
    max_tokens: int = 2048        # Max response length
    system_prompt: str = (
        "You are a 5G NR SA Core protocol expert assistant for the SA Tester "
        "platform by MakeMyTechnology. You specialize in NGAP (TS 38.413), "
        "NAS 5GS (TS 24.501), SCTP transport (RFC 4960), 5G security (TS 33.501), "
        "and 5G system architecture (TS 23.501). "
        "Answer questions about 3GPP specifications, 5G SA procedures, "
        "protocol stack layers, PCAP/Wireshark analysis, and test results. "
        "Be precise, cite relevant 3GPP spec sections when possible, and explain "
        "concepts clearly for engineers."
    )


class OllamaClient:
    """
    REST client for the local Ollama API.

    Provides:
    - Text generation (chat and completion)
    - Text embeddings for RAG
    - Model management (list, pull, status)
    - Log analysis (SA tester logs + Wireshark PCAPs)
    """

    def __init__(self, config: Optional[OllamaConfig] = None):
        self._config = config or OllamaConfig()
        self._session = requests.Session()

    @property
    def config(self) -> OllamaConfig:
        return self._config

    @property
    def is_available(self) -> bool:
        """Check if Ollama is running and reachable."""
        try:
            resp = self._session.get(
                f"{self._config.base_url}/api/tags",
                timeout=3,
            )
            return resp.status_code == 200
        except requests.RequestException:
            return False

    def health_check(self) -> dict:
        """Check Ollama status and list available models."""
        try:
            resp = self._session.get(
                f"{self._config.base_url}/api/tags",
                timeout=5,
            )
            if resp.status_code == 200:
                data = resp.json()
                models = [m.get("name", "") for m in data.get("models", [])]
                return {
                    "status": "available",
                    "url": self._config.base_url,
                    "models": models,
                    "configured_model": self._config.model,
                    "model_loaded": self._config.model in models,
                }
            return {"status": "error", "status_code": resp.status_code}
        except requests.ConnectionError:
            return {"status": "unavailable", "url": self._config.base_url}
        except requests.RequestException as e:
            return {"status": "error", "error": str(e)}

    def generate(self, prompt: str,
                 system: str = "",
                 context: str = "",
                 model: str = "",
                 temperature: float = None,
                 max_tokens: int = None,
                 timeout: int = None) -> dict:
        """
        Generate text using the LLM.

        Args:
            prompt: User question/prompt
            system: Override system prompt
            context: Additional context (RAG-retrieved docs, logs, PCAP)
            model: Override model name
            temperature: Override temperature
            max_tokens: Override max tokens

        Returns:
            dict with 'response', 'model', 'total_duration_ms', etc.
        """
        model = model or self._config.model
        system = system or self._config.system_prompt
        temp = temperature if temperature is not None else self._config.temperature
        tokens = max_tokens or self._config.max_tokens

        full_prompt = prompt
        if context:
            full_prompt = (
                f"Use the following context to answer the question.\n\n"
                f"Context:\n{context}\n\n"
                f"Question: {prompt}"
            )

        try:
            resp = self._session.post(
                f"{self._config.base_url}/api/generate",
                json={
                    "model": model,
                    "prompt": full_prompt,
                    "system": system,
                    "stream": False,
                    "options": {
                        "temperature": temp,
                        "num_predict": tokens,
                    },
                },
                timeout=timeout or self._config.timeout,
            )

            if resp.status_code == 200:
                data = resp.json()
                return {
                    "response": data.get("response", ""),
                    "model": data.get("model", model),
                    "total_duration_ms": data.get("total_duration", 0) / 1_000_000,
                    "eval_count": data.get("eval_count", 0),
                    "prompt_eval_count": data.get("prompt_eval_count", 0),
                }
            return {"response": "", "error": f"Ollama error: {resp.status_code}"}

        except requests.Timeout:
            return {"response": "", "error": "Generation timed out"}
        except requests.RequestException as e:
            return {"response": "", "error": str(e)}

    def chat(self, messages: List[dict],
             model: str = "",
             temperature: float = None) -> dict:
        """
        Chat-style generation with message history.

        Args:
            messages: List of {"role": "user"|"assistant"|"system", "content": "..."}
            model: Override model name
            temperature: Override temperature
        """
        model = model or self._config.model
        temp = temperature if temperature is not None else self._config.temperature

        if not any(m.get("role") == "system" for m in messages):
            messages = [{"role": "system", "content": self._config.system_prompt}] + messages

        try:
            resp = self._session.post(
                f"{self._config.base_url}/api/chat",
                json={
                    "model": model,
                    "messages": messages,
                    "stream": False,
                    "options": {
                        "temperature": temp,
                        "num_predict": self._config.max_tokens,
                    },
                },
                timeout=self._config.timeout,
            )

            if resp.status_code == 200:
                data = resp.json()
                msg = data.get("message", {})
                return {
                    "response": msg.get("content", ""),
                    "model": data.get("model", model),
                    "total_duration_ms": data.get("total_duration", 0) / 1_000_000,
                }
            return {"response": "", "error": f"Ollama error: {resp.status_code}"}

        except requests.RequestException as e:
            return {"response": "", "error": str(e)}

    def embed(self, text: str, model: str = "") -> Optional[List[float]]:
        """Generate embedding vector for text. Returns list of floats or None."""
        model = model or self._config.embedding_model
        try:
            resp = self._session.post(
                f"{self._config.base_url}/api/embed",
                json={"model": model, "input": text},
                timeout=self._config.embed_timeout,
            )
            if resp.status_code == 200:
                data = resp.json()
                embeddings = data.get("embeddings", [])
                return embeddings[0] if embeddings else None
            return None
        except requests.RequestException:
            return None

    def embed_batch(self, texts: List[str], model: str = "") -> List[Optional[List[float]]]:
        """Generate embeddings for multiple texts."""
        return [self.embed(t, model=model) for t in texts]

    def list_models(self) -> List[dict]:
        """List all locally available models."""
        try:
            resp = self._session.get(
                f"{self._config.base_url}/api/tags", timeout=5,
            )
            if resp.status_code == 200:
                return resp.json().get("models", [])
            return []
        except requests.RequestException:
            return []

    def pull_model(self, model: str) -> dict:
        """Pull (download) a model from Ollama registry."""
        try:
            resp = self._session.post(
                f"{self._config.base_url}/api/pull",
                json={"name": model, "stream": False},
                timeout=600,
            )
            if resp.status_code == 200:
                return {"status": "ok", "model": model}
            return {"status": "error", "error": resp.text}
        except requests.RequestException as e:
            return {"status": "error", "error": str(e)}

    def analyze_log(self, log_events: List[dict], question: str = "") -> dict:
        """
        AI-powered analysis of SA tester log events.

        Args:
            log_events: List of log entry dicts (from ring buffer or PCAP)
            question: Specific question about the logs
        """
        summary_lines = []
        for i, e in enumerate(log_events[:80]):
            ts = e.get("timestamp", "")
            level = e.get("level", "")
            logger = e.get("logger_name", "")
            msg = e.get("message", "")[:120]
            summary_lines.append(f"{i+1}. [{level}] {logger}: {msg}")

        log_text = "\n".join(summary_lines)
        prompt = question or (
            "Analyze these 5G SA tester log events. Identify the NGAP/NAS procedures, "
            "check for errors or failures, and summarize what happened."
        )

        return self.generate(
            prompt=prompt,
            context=f"SA Tester Log Events ({len(log_events)} total, showing first 80):\n\n{log_text}",
            system=(
                "You are a 5G NR SA Core protocol expert analyzing logs from an NGAP/NAS tester. "
                "Identify procedures (NG Setup, Registration, PDU Session Establishment, Deregistration), "
                "SCTP association events, NGAP message exchanges, NAS security procedures, "
                "flag errors or failures, and provide a clear summary. "
                "Reference 3GPP specs (TS 38.413, TS 24.501, TS 33.501) where applicable."
            ),
        )

    def analyze_pcap(self, pcap_summary: str, question: str = "") -> dict:
        """
        AI-powered analysis of Wireshark/tshark PCAP data.

        Args:
            pcap_summary: Text summary of decoded PCAP messages
            question: Specific question about the capture
        """
        prompt = question or (
            "Analyze this Wireshark PCAP capture of 5G SA Core signaling. "
            "Identify the NGAP procedures, NAS messages, SCTP events, "
            "check the message flow for correctness, and flag any anomalies."
        )

        return self.generate(
            prompt=prompt,
            context=f"Wireshark PCAP Decoded Messages:\n\n{pcap_summary}",
            system=(
                "You are a 5G NR protocol expert analyzing Wireshark PCAP captures. "
                "The capture contains SCTP/NGAP/NAS-5GS signaling between gNB and AMF "
                "on port 38412. Identify the complete message flow, verify correct "
                "procedure sequencing per 3GPP TS 38.413 and TS 24.501, flag any "
                "missing or unexpected messages, and provide a verdict. "
                "Use a structured format: Procedure → Messages → Verdict."
            ),
        )
