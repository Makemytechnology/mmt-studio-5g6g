# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""
PcapAnalyzer — Wireshark/tshark PCAP analysis for SA Tester
=============================================================
Parses PCAP files using tshark (Wireshark CLI) and prepares
protocol-specific summaries for AI-assisted analysis.

Supports:
    - SCTP association analysis
    - NGAP message extraction and decoding
    - NAS-5G message extraction
    - Protocol statistics and flow summaries
    - AI-powered analysis via OllamaClient

Usage:
    analyzer = PcapAnalyzer(ollama_client)
    summary = analyzer.analyze_pcap("/path/to/capture.pcap")
    ngap_msgs = analyzer.extract_ngap_messages("/path/to/capture.pcap")
"""

import os
import json
import subprocess
import logging
import re
import tempfile
from typing import Dict, List, Optional
from dataclasses import dataclass, field

log = logging.getLogger("tester.ai.pcap")


@dataclass
class ProtocolMessage:
    """A single protocol message extracted from a PCAP."""
    frame_number: int
    timestamp: str
    src_ip: str
    dst_ip: str
    protocol: str
    info: str
    raw_hex: str = ""
    decoded: Dict = field(default_factory=dict)


@dataclass
class SctpAssociation:
    """SCTP association details from a PCAP."""
    src_ip: str
    dst_ip: str
    src_port: int
    dst_port: int
    init_tag: str = ""
    streams: int = 0
    chunks_sent: int = 0
    chunks_recv: int = 0


@dataclass
class PcapSummary:
    """Complete summary of a PCAP file analysis."""
    filename: str
    total_frames: int = 0
    duration_sec: float = 0.0
    protocols: Dict[str, int] = field(default_factory=dict)
    sctp_associations: List[SctpAssociation] = field(default_factory=list)
    ngap_messages: List[ProtocolMessage] = field(default_factory=list)
    nas_messages: List[ProtocolMessage] = field(default_factory=list)
    errors: List[str] = field(default_factory=list)
    ai_analysis: str = ""


class PcapAnalyzer:
    """
    Analyzes Wireshark PCAP captures for 5G SA Core protocols.

    Uses tshark for PCAP parsing and optional OllamaClient for
    AI-powered analysis of protocol flows and anomalies.
    """

    # Common tshark paths on Windows
    TSHARK_PATHS = [
        r"C:\Program Files\Wireshark\tshark.exe",
        r"C:\Program Files (x86)\Wireshark\tshark.exe",
    ]

    # NGAP procedure codes (TS 38.413)
    NGAP_PROCEDURES = {
        0: "AMFConfigurationUpdate",
        1: "AMFStatusIndication",
        2: "CellTrafficTrace",
        3: "DeactivateTrace",
        4: "DownlinkNASTransport",
        5: "DownlinkNonUEAssociatedNRPPaTransport",
        6: "DownlinkRANConfigurationTransfer",
        7: "DownlinkRANStatusTransfer",
        8: "DownlinkUEAssociatedNRPPaTransport",
        9: "ErrorIndication",
        10: "HandoverCancel",
        11: "HandoverNotification",
        12: "HandoverPreparation",
        13: "HandoverResourceAllocation",
        14: "InitialContextSetup",
        15: "InitialUEMessage",
        16: "LocationReportingControl",
        17: "LocationReportingFailureIndication",
        18: "LocationReport",
        19: "NASNonDeliveryIndication",
        20: "NGReset",
        21: "NGSetup",
        22: "OverloadStart",
        23: "OverloadStop",
        24: "Paging",
        25: "PathSwitchRequest",
        26: "PDUSessionResourceModify",
        27: "PDUSessionResourceModifyIndication",
        28: "PDUSessionResourceRelease",
        29: "PDUSessionResourceSetup",
        30: "PWSCancel",
        31: "PWSFailureIndication",
        32: "PWSRestartIndication",
        33: "RANConfigurationUpdate",
        34: "RerouteNASRequest",
        35: "RRCInactiveTransitionReport",
        36: "TraceFailureIndication",
        37: "TraceStart",
        38: "UEContextModification",
        39: "UEContextRelease",
        40: "UEContextReleaseRequest",
        41: "UERadioCapabilityCheck",
        42: "UERadioCapabilityInfoIndication",
        43: "UETNLABindingRelease",
        44: "UplinkNASTransport",
        45: "UplinkNonUEAssociatedNRPPaTransport",
        46: "UplinkRANConfigurationTransfer",
        47: "UplinkRANStatusTransfer",
        48: "UplinkUEAssociatedNRPPaTransport",
        49: "WriteReplaceWarning",
    }

    def __init__(self, ollama_client=None, tshark_path: str = ""):
        self._llm = ollama_client
        self._tshark = tshark_path or self._find_tshark()

    def _find_tshark(self) -> str:
        """Locate tshark binary."""
        # Check PATH first
        try:
            result = subprocess.run(
                ["tshark", "--version"],
                capture_output=True, text=True, timeout=5
            )
            if result.returncode == 0:
                return "tshark"
        except (FileNotFoundError, subprocess.TimeoutExpired):
            pass

        # Check common Windows paths
        for path in self.TSHARK_PATHS:
            if os.path.isfile(path):
                return path

        log.warning("tshark not found — PCAP analysis will be limited")
        return ""

    @property
    def tshark_available(self) -> bool:
        return bool(self._tshark)

    def analyze_pcap(self, pcap_path: str, ai_analyze: bool = True) -> PcapSummary:
        """
        Full analysis of a PCAP file.

        Args:
            pcap_path: Path to .pcap or .pcapng file
            ai_analyze: If True and LLM available, add AI analysis

        Returns:
            PcapSummary with protocol breakdown, messages, and optional AI analysis
        """
        if not os.path.isfile(pcap_path):
            return PcapSummary(
                filename=pcap_path,
                errors=[f"File not found: {pcap_path}"]
            )

        summary = PcapSummary(filename=os.path.basename(pcap_path))

        if not self._tshark:
            summary.errors.append("tshark not available — install Wireshark for full PCAP analysis")
            # Try to provide basic file info
            summary.total_frames = 0
            return summary

        try:
            # Get basic statistics
            self._get_pcap_stats(pcap_path, summary)

            # Extract protocol hierarchy
            self._get_protocol_hierarchy(pcap_path, summary)

            # Extract SCTP associations
            self._get_sctp_associations(pcap_path, summary)

            # Extract NGAP messages
            self._get_ngap_messages(pcap_path, summary)

            # Extract NAS-5G messages
            self._get_nas_messages(pcap_path, summary)

        except Exception as e:
            summary.errors.append(f"Analysis error: {e}")
            log.error("PCAP analysis failed: %s", e, exc_info=True)

        # AI-powered analysis
        if ai_analyze and self._llm and self._llm.is_available:
            try:
                summary.ai_analysis = self._ai_analyze(summary)
            except Exception as e:
                summary.errors.append(f"AI analysis error: {e}")
                log.error("AI PCAP analysis failed: %s", e)

        return summary

    def extract_ngap_messages(self, pcap_path: str) -> List[ProtocolMessage]:
        """Extract only NGAP messages from a PCAP."""
        if not self._tshark or not os.path.isfile(pcap_path):
            return []

        summary = PcapSummary(filename=pcap_path)
        self._get_ngap_messages(pcap_path, summary)
        return summary.ngap_messages

    def extract_nas_messages(self, pcap_path: str) -> List[ProtocolMessage]:
        """Extract only NAS-5G messages from a PCAP."""
        if not self._tshark or not os.path.isfile(pcap_path):
            return []

        summary = PcapSummary(filename=pcap_path)
        self._get_nas_messages(pcap_path, summary)
        return summary.nas_messages

    def get_flow_text(self, pcap_path: str) -> str:
        """
        Generate a human-readable text flow from a PCAP.
        Suitable for feeding to LLM for analysis.
        """
        if not self._tshark or not os.path.isfile(pcap_path):
            return ""

        try:
            result = subprocess.run(
                [self._tshark, "-r", pcap_path,
                 "-Y", "ngap || nas-5gs || sctp",
                 "-T", "fields",
                 "-e", "frame.number",
                 "-e", "frame.time_relative",
                 "-e", "ip.src",
                 "-e", "ip.dst",
                 "-e", "_ws.col.Protocol",
                 "-e", "_ws.col.Info",
                 "-E", "separator=|",
                 "-E", "header=y"],
                capture_output=True, text=True, timeout=30
            )
            if result.returncode == 0:
                return result.stdout
        except (subprocess.TimeoutExpired, Exception) as e:
            log.error("Flow text extraction failed: %s", e)

        return ""

    def analyze_with_ai(self, pcap_path: str, question: str = "") -> str:
        """
        AI-powered analysis of a PCAP file.

        Args:
            pcap_path: Path to PCAP file
            question: Specific question about the capture (optional)

        Returns:
            AI analysis text
        """
        if not self._llm or not self._llm.is_available:
            return "AI engine not available (Ollama not running)"

        flow_text = self.get_flow_text(pcap_path)
        if not flow_text:
            return "Could not extract protocol data from PCAP"

        # Build a concise summary for the LLM
        summary = self.analyze_pcap(pcap_path, ai_analyze=False)
        context = self._build_context(summary, flow_text)

        prompt = question or (
            "Analyze this 5G SA Core PCAP capture. Identify the protocol flow, "
            "check for any anomalies, verify NG Setup / Registration procedures "
            "follow 3GPP TS 38.413 / TS 24.501 specifications, and highlight "
            "any issues found."
        )

        result = self._llm.generate(
            prompt=prompt,
            context=context,
            system=(
                "You are a 5G NR SA Core protocol expert analyzing Wireshark captures. "
                "Focus on SCTP, NGAP (TS 38.413), and NAS-5G (TS 24.501) protocols. "
                "Identify procedure flows, verify compliance, and flag anomalies. "
                "Be specific about message types, procedure codes, and IEs."
            ),
        )
        return result.get("response", "No analysis generated")

    # ── Internal methods ──

    def _get_pcap_stats(self, pcap_path: str, summary: PcapSummary):
        """Get basic PCAP statistics."""
        try:
            result = subprocess.run(
                [self._tshark, "-r", pcap_path, "-q", "-z", "io,stat,0"],
                capture_output=True, text=True, timeout=15
            )
            if result.returncode == 0:
                output = result.stdout
                # Parse frame count and duration
                for line in output.split("\n"):
                    if "|" in line and "<>" in line:
                        parts = line.split("|")
                        if len(parts) >= 3:
                            try:
                                summary.total_frames = int(parts[1].strip())
                            except ValueError:
                                pass

            # Also get capinfos-style stats
            result2 = subprocess.run(
                [self._tshark, "-r", pcap_path, "-q", "-z", "io,stat,0,COUNT(frame)frame"],
                capture_output=True, text=True, timeout=15
            )
            # Parse total frames from packet count
            count_result = subprocess.run(
                [self._tshark, "-r", pcap_path, "-T", "fields", "-e", "frame.number"],
                capture_output=True, text=True, timeout=15
            )
            if count_result.returncode == 0:
                lines = [l.strip() for l in count_result.stdout.strip().split("\n") if l.strip()]
                if lines:
                    try:
                        summary.total_frames = int(lines[-1])
                    except ValueError:
                        summary.total_frames = len(lines)

        except (subprocess.TimeoutExpired, Exception) as e:
            summary.errors.append(f"Stats extraction failed: {e}")

    def _get_protocol_hierarchy(self, pcap_path: str, summary: PcapSummary):
        """Get protocol hierarchy statistics."""
        try:
            result = subprocess.run(
                [self._tshark, "-r", pcap_path, "-q", "-z", "io,phs"],
                capture_output=True, text=True, timeout=15
            )
            if result.returncode == 0:
                for line in result.stdout.split("\n"):
                    line = line.strip()
                    if line and "frames:" in line:
                        # Parse "  protocol  frames:N bytes:M"
                        match = re.match(r"(\S+)\s+frames:(\d+)", line)
                        if match:
                            proto = match.group(1)
                            count = int(match.group(2))
                            summary.protocols[proto] = count

        except (subprocess.TimeoutExpired, Exception) as e:
            summary.errors.append(f"Protocol hierarchy failed: {e}")

    def _get_sctp_associations(self, pcap_path: str, summary: PcapSummary):
        """Extract SCTP association details."""
        try:
            result = subprocess.run(
                [self._tshark, "-r", pcap_path,
                 "-Y", "sctp.chunk_type == 1",  # INIT chunks
                 "-T", "fields",
                 "-e", "ip.src", "-e", "ip.dst",
                 "-e", "sctp.srcport", "-e", "sctp.dstport",
                 "-e", "sctp.init_tag",
                 "-e", "sctp.init_nr_out_streams",
                 "-E", "separator=|"],
                capture_output=True, text=True, timeout=15
            )
            if result.returncode == 0:
                for line in result.stdout.strip().split("\n"):
                    if not line.strip():
                        continue
                    parts = line.split("|")
                    if len(parts) >= 4:
                        try:
                            assoc = SctpAssociation(
                                src_ip=parts[0].strip(),
                                dst_ip=parts[1].strip(),
                                src_port=int(parts[2].strip()) if parts[2].strip() else 0,
                                dst_port=int(parts[3].strip()) if parts[3].strip() else 0,
                                init_tag=parts[4].strip() if len(parts) > 4 else "",
                                streams=int(parts[5].strip()) if len(parts) > 5 and parts[5].strip() else 0,
                            )
                            summary.sctp_associations.append(assoc)
                        except (ValueError, IndexError):
                            continue

        except (subprocess.TimeoutExpired, Exception) as e:
            summary.errors.append(f"SCTP extraction failed: {e}")

    def _get_ngap_messages(self, pcap_path: str, summary: PcapSummary):
        """Extract NGAP messages."""
        try:
            result = subprocess.run(
                [self._tshark, "-r", pcap_path,
                 "-Y", "ngap",
                 "-T", "fields",
                 "-e", "frame.number",
                 "-e", "frame.time_relative",
                 "-e", "ip.src", "-e", "ip.dst",
                 "-e", "_ws.col.Info",
                 "-e", "ngap.procedureCode",
                 "-E", "separator=|"],
                capture_output=True, text=True, timeout=15
            )
            if result.returncode == 0:
                for line in result.stdout.strip().split("\n"):
                    if not line.strip():
                        continue
                    parts = line.split("|")
                    if len(parts) >= 5:
                        try:
                            msg = ProtocolMessage(
                                frame_number=int(parts[0].strip()) if parts[0].strip() else 0,
                                timestamp=parts[1].strip(),
                                src_ip=parts[2].strip(),
                                dst_ip=parts[3].strip(),
                                protocol="NGAP",
                                info=parts[4].strip(),
                            )
                            # Add procedure code to decoded
                            if len(parts) > 5 and parts[5].strip():
                                try:
                                    pc = int(parts[5].strip())
                                    msg.decoded["procedureCode"] = pc
                                    msg.decoded["procedureName"] = self.NGAP_PROCEDURES.get(pc, f"Unknown({pc})")
                                except ValueError:
                                    pass
                            summary.ngap_messages.append(msg)
                        except (ValueError, IndexError):
                            continue

        except (subprocess.TimeoutExpired, Exception) as e:
            summary.errors.append(f"NGAP extraction failed: {e}")

    def _get_nas_messages(self, pcap_path: str, summary: PcapSummary):
        """Extract NAS-5G messages."""
        try:
            result = subprocess.run(
                [self._tshark, "-r", pcap_path,
                 "-Y", "nas-5gs",
                 "-T", "fields",
                 "-e", "frame.number",
                 "-e", "frame.time_relative",
                 "-e", "ip.src", "-e", "ip.dst",
                 "-e", "_ws.col.Info",
                 "-e", "nas_5gs.mm.message_type",
                 "-E", "separator=|"],
                capture_output=True, text=True, timeout=15
            )
            if result.returncode == 0:
                for line in result.stdout.strip().split("\n"):
                    if not line.strip():
                        continue
                    parts = line.split("|")
                    if len(parts) >= 5:
                        try:
                            msg = ProtocolMessage(
                                frame_number=int(parts[0].strip()) if parts[0].strip() else 0,
                                timestamp=parts[1].strip(),
                                src_ip=parts[2].strip(),
                                dst_ip=parts[3].strip(),
                                protocol="NAS-5GS",
                                info=parts[4].strip(),
                            )
                            if len(parts) > 5 and parts[5].strip():
                                msg.decoded["message_type"] = parts[5].strip()
                            summary.nas_messages.append(msg)
                        except (ValueError, IndexError):
                            continue

        except (subprocess.TimeoutExpired, Exception) as e:
            summary.errors.append(f"NAS extraction failed: {e}")

    def _build_context(self, summary: PcapSummary, flow_text: str) -> str:
        """Build context string for AI analysis."""
        parts = [
            f"PCAP File: {summary.filename}",
            f"Total Frames: {summary.total_frames}",
        ]

        if summary.protocols:
            parts.append("\nProtocol Hierarchy:")
            for proto, count in sorted(summary.protocols.items(), key=lambda x: -x[1]):
                parts.append(f"  {proto}: {count} frames")

        if summary.sctp_associations:
            parts.append("\nSCTP Associations:")
            for a in summary.sctp_associations:
                parts.append(f"  {a.src_ip}:{a.src_port} -> {a.dst_ip}:{a.dst_port} (streams={a.streams})")

        if summary.ngap_messages:
            parts.append(f"\nNGAP Messages ({len(summary.ngap_messages)}):")
            for m in summary.ngap_messages[:50]:  # Limit for context window
                proc = m.decoded.get("procedureName", "")
                parts.append(f"  #{m.frame_number} [{m.timestamp}s] {m.src_ip}->{m.dst_ip}: {m.info} {proc}")

        if summary.nas_messages:
            parts.append(f"\nNAS-5G Messages ({len(summary.nas_messages)}):")
            for m in summary.nas_messages[:50]:
                parts.append(f"  #{m.frame_number} [{m.timestamp}s] {m.src_ip}->{m.dst_ip}: {m.info}")

        if flow_text:
            parts.append("\nDetailed Protocol Flow:")
            # Truncate to avoid context window overflow
            flow_lines = flow_text.split("\n")
            if len(flow_lines) > 200:
                parts.append("\n".join(flow_lines[:200]))
                parts.append(f"... ({len(flow_lines) - 200} more lines)")
            else:
                parts.append(flow_text)

        return "\n".join(parts)

    def _ai_analyze(self, summary: PcapSummary) -> str:
        """Run AI analysis on a PCAP summary."""
        context = self._build_context(summary, "")

        prompt = (
            "Analyze this 5G SA Core network capture. "
            "Identify the procedures being executed (NG Setup, Registration, "
            "PDU Session Establishment, etc.), verify the message sequence "
            "follows 3GPP specifications, and flag any anomalies or issues."
        )

        result = self._llm.generate(
            prompt=prompt,
            context=context,
            system=(
                "You are a 5G NR SA Core protocol expert. Analyze PCAP summaries "
                "focusing on SCTP (RFC 4960), NGAP (TS 38.413), and NAS-5G (TS 24.501). "
                "Check procedure flows, message ordering, mandatory IEs, and timing. "
                "Flag any protocol violations or anomalies."
            ),
        )
        return result.get("response", "")

    def to_dict(self, summary: PcapSummary) -> dict:
        """Convert PcapSummary to JSON-serializable dict."""
        return {
            "filename": summary.filename,
            "total_frames": summary.total_frames,
            "duration_sec": summary.duration_sec,
            "protocols": summary.protocols,
            "sctp_associations": [
                {
                    "src_ip": a.src_ip, "dst_ip": a.dst_ip,
                    "src_port": a.src_port, "dst_port": a.dst_port,
                    "init_tag": a.init_tag, "streams": a.streams,
                }
                for a in summary.sctp_associations
            ],
            "ngap_messages": [
                {
                    "frame": m.frame_number, "time": m.timestamp,
                    "src": m.src_ip, "dst": m.dst_ip,
                    "info": m.info, "decoded": m.decoded,
                }
                for m in summary.ngap_messages
            ],
            "nas_messages": [
                {
                    "frame": m.frame_number, "time": m.timestamp,
                    "src": m.src_ip, "dst": m.dst_ip,
                    "info": m.info, "decoded": m.decoded,
                }
                for m in summary.nas_messages
            ],
            "errors": summary.errors,
            "ai_analysis": summary.ai_analysis,
        }
