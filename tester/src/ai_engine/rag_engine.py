# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""
RAGEngine -- Retrieval-Augmented Generation for 3GPP Protocol Q&A
==================================================================
Embeds 3GPP spec chunks into a local vector store, retrieves relevant
passages for user queries, and generates answers via Ollama LLM.

Vector store: Simple in-memory store using cosine similarity.
Persisted to JSON for offline use.

Usage:
    rag = RAGEngine(ollama_client)
    rag.index_text("ts38413", "8.7.1", "NG Setup procedure text...")
    answer = rag.query("How does NG Setup work?")
"""

import math
import time
import os
import json
import logging
from typing import Dict, List, Optional, Tuple
from dataclasses import dataclass, field

log = logging.getLogger("tester.ai.rag")


@dataclass
class DocChunk:
    """A chunk of text from a 3GPP spec or reference document."""
    chunk_id: str
    doc_id: str           # e.g., "ts38413", "ts24501"
    section: str          # e.g., "8.7.1 NG Setup"
    text: str
    embedding: Optional[List[float]] = None
    metadata: Dict = field(default_factory=dict)


@dataclass
class SearchResult:
    """A retrieved chunk with similarity score."""
    chunk: DocChunk
    score: float          # cosine similarity [0, 1]


@dataclass
class RAGResponse:
    """Response from the RAG pipeline."""
    answer: str
    sources: List[SearchResult]
    model: str
    query: str
    duration_ms: float


class VectorStore:
    """
    Simple in-memory vector store using cosine similarity.
    Stores embeddings alongside document chunks.
    """

    def __init__(self):
        self._chunks: List[DocChunk] = []
        self._persist_path: Optional[str] = None

    @property
    def size(self) -> int:
        return len(self._chunks)

    def set_persist_path(self, path: str):
        self._persist_path = path

    def add(self, chunk: DocChunk):
        if chunk.embedding is None:
            raise ValueError(f"Chunk {chunk.chunk_id} has no embedding")
        self._chunks.append(chunk)

    def add_batch(self, chunks: List[DocChunk]):
        for c in chunks:
            self.add(c)

    def search(self, query_embedding: List[float],
               top_k: int = 5,
               doc_filter: str = "") -> List[SearchResult]:
        results = []
        for chunk in self._chunks:
            if doc_filter and chunk.doc_id != doc_filter:
                continue
            if chunk.embedding is None:
                continue
            score = self._cosine_similarity(query_embedding, chunk.embedding)
            results.append(SearchResult(chunk=chunk, score=score))
        results.sort(key=lambda r: r.score, reverse=True)
        return results[:top_k]

    def get_docs(self) -> List[str]:
        return list(set(c.doc_id for c in self._chunks))

    def get_doc_chunks(self, doc_id: str) -> List[DocChunk]:
        return [c for c in self._chunks if c.doc_id == doc_id]

    def remove_doc(self, doc_id: str) -> int:
        before = len(self._chunks)
        self._chunks = [c for c in self._chunks if c.doc_id != doc_id]
        return before - len(self._chunks)

    def clear(self):
        self._chunks.clear()

    def save(self, path: str = ""):
        path = path or self._persist_path
        if not path:
            return
        data = []
        for c in self._chunks:
            data.append({
                "chunk_id": c.chunk_id,
                "doc_id": c.doc_id,
                "section": c.section,
                "text": c.text,
                "embedding": c.embedding,
                "metadata": c.metadata,
            })
        os.makedirs(os.path.dirname(path) or ".", exist_ok=True)
        with open(path, "w", encoding="utf-8") as f:
            json.dump(data, f)

    def load(self, path: str = ""):
        path = path or self._persist_path
        if not path or not os.path.exists(path):
            return
        with open(path, "r", encoding="utf-8") as f:
            data = json.load(f)
        self._chunks.clear()
        for item in data:
            self._chunks.append(DocChunk(
                chunk_id=item["chunk_id"],
                doc_id=item["doc_id"],
                section=item.get("section", ""),
                text=item["text"],
                embedding=item.get("embedding"),
                metadata=item.get("metadata", {}),
            ))

    @staticmethod
    def _cosine_similarity(a: List[float], b: List[float]) -> float:
        if len(a) != len(b) or not a:
            return 0.0
        dot = sum(x * y for x, y in zip(a, b))
        norm_a = math.sqrt(sum(x * x for x in a))
        norm_b = math.sqrt(sum(x * x for x in b))
        if norm_a == 0 or norm_b == 0:
            return 0.0
        return dot / (norm_a * norm_b)


class RAGEngine:
    """
    Retrieval-Augmented Generation pipeline.

    Flow:
        1. User query -> embed via Ollama
        2. Search vector store for relevant chunks
        3. Build prompt with retrieved context
        4. Generate answer via Ollama LLM
        5. Return answer with source citations
    """

    def __init__(self, ollama_client, store_path: str = ""):
        self._llm = ollama_client
        self._store = VectorStore()
        if store_path:
            self._store.set_persist_path(store_path)
            self._store.load()

    @property
    def store(self) -> VectorStore:
        return self._store

    @property
    def doc_count(self) -> int:
        return len(self._store.get_docs())

    @property
    def chunk_count(self) -> int:
        return self._store.size

    def index_text(self, doc_id: str, section: str, text: str,
                   chunk_size: int = 500, overlap: int = 50) -> int:
        chunks = self._split_text(text, chunk_size, overlap)
        count = 0
        for i, chunk_text in enumerate(chunks):
            embedding = self._llm.embed(chunk_text)
            if embedding is None:
                continue
            chunk = DocChunk(
                chunk_id=f"{doc_id}_s{section}_{i:04d}",
                doc_id=doc_id,
                section=section,
                text=chunk_text,
                embedding=embedding,
                metadata={"char_offset": i * (chunk_size - overlap)},
            )
            self._store.add(chunk)
            count += 1
        return count

    def index_chunks(self, doc_id: str, chunks: List[Dict]) -> int:
        count = 0
        for i, c in enumerate(chunks):
            text = c.get("text", "")
            if not text.strip():
                continue
            section = c.get("section", "")
            embed_text = f"{section}: {text}" if section else text
            embedding = self._llm.embed(embed_text)
            if embedding is None:
                continue
            chunk = DocChunk(
                chunk_id=f"{doc_id}_{i:04d}",
                doc_id=doc_id,
                section=section,
                text=text,
                embedding=embedding,
                metadata=c.get("metadata", {}),
            )
            self._store.add(chunk)
            count += 1
        return count

    def query(self, question: str, top_k: int = 8,
              doc_filter: str = "", temperature: float = None) -> RAGResponse:
        start = time.time()
        query_emb = self._llm.embed(question)
        sources = []
        context_text = ""
        if query_emb and self._store.size > 0:
            sources = self._store.search(query_emb, top_k=top_k, doc_filter=doc_filter)
            context_parts = []
            for i, sr in enumerate(sources):
                header = f"[Source {i+1}: {sr.chunk.doc_id} - {sr.chunk.section}]"
                context_parts.append(f"{header}\n{sr.chunk.text}")
            context_text = "\n\n".join(context_parts)

        system = (
            "You are a 5G NR SA Core protocol expert for the SA Tester platform. "
            "Answer based on the provided context from 3GPP specifications. "
            "Cite the source document and section when referencing specific information. "
            "If the context doesn't contain enough information, say so clearly."
        )

        result = self._llm.generate(
            prompt=question, context=context_text,
            system=system, temperature=temperature,
        )
        elapsed = (time.time() - start) * 1000

        return RAGResponse(
            answer=result.get("response", ""),
            sources=sources,
            model=result.get("model", ""),
            query=question,
            duration_ms=elapsed,
        )

    def query_no_rag(self, question: str, temperature: float = None) -> RAGResponse:
        start = time.time()
        result = self._llm.generate(prompt=question, temperature=temperature)
        elapsed = (time.time() - start) * 1000
        return RAGResponse(
            answer=result.get("response", ""),
            sources=[], model=result.get("model", ""),
            query=question, duration_ms=elapsed,
        )

    def save(self):
        self._store.save()

    def get_status(self) -> dict:
        return {
            "llm_available": self._llm.is_available,
            "documents": self._store.get_docs(),
            "total_chunks": self._store.size,
            "llm_health": self._llm.health_check(),
        }

    @staticmethod
    def _split_text(text: str, chunk_size: int = 500, overlap: int = 50) -> List[str]:
        if len(text) <= chunk_size:
            return [text] if text.strip() else []
        chunks = []
        start = 0
        while start < len(text):
            end = start + chunk_size
            if end < len(text):
                search_start = end - int(chunk_size * 0.2)
                best_break = -1
                for sep in [". ", ".\n", "\n\n", "\n"]:
                    idx = text.rfind(sep, search_start, end)
                    if idx > best_break:
                        best_break = idx + len(sep)
                if best_break > start:
                    end = best_break
            chunk = text[start:end].strip()
            if chunk:
                chunks.append(chunk)
            start = end - overlap
            if start >= len(text):
                break
        return chunks
