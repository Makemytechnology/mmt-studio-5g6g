FROM python:3.12-slim

# System dependencies for SCTP, GTP-U tunnels, iperf3.
# iputils-ping ships /bin/ping — TC-IMS-005 invokes it directly to
# measure 5QI=1 conversational-voice RTT against the TS 23.501
# §5.7.2.1 / Table 5.7.4-1 Packet Delay Budget (100 ms). Without it
# subprocess.run(["ping", ...]) fails ENOENT on the slim image.
RUN apt-get update && apt-get install -y --no-install-recommends \
    gcc \
    libsctp-dev \
    lksctp-tools \
    iperf3 \
    iproute2 \
    iputils-ping \
    net-tools \
    tcpdump \
    && rm -rf /var/lib/apt/lists/*
# tcpdump above: TestRunner wraps each test case with a tcpdump
# subprocess capturing satester's mmtnet interface for the lifetime
# of the test. The per-test pcap is saved to data/test_results/ and
# streamed live via GET /api/tests/active/pcap.stream so Wireshark
# on the operator's host can see packets as they arrive. Capture
# inside the test container (instead of a docker sidecar) eliminates
# all the race conditions of the previous architecture: the test
# itself controls capture start/stop with millisecond precision.

WORKDIR /app

# Install Python dependencies
COPY build/requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt

# Copy application code
COPY . .

# Add project root and libs to Python path. pycrate's vendored
# sub-packages (pycrate_asn1dir / pycrate_asn1rt / pycrate_corenet /
# pycrate_core / pycrate_asn1c) sit one level under libs/pycrate, so
# both directories must be on the path — otherwise `from
# pycrate_asn1dir.X import Y` fails and every TC that touches NGAP /
# NAS / SUCI ASN.1 (tc_ng_setup, tc_ims, tc_handover, ...) is silently
# skipped at registry import time. Matches the Windows entry in
# build/windows/__main__.py which adds the same three roots.
ENV PYTHONPATH="/app:/app/libs:/app/libs/pycrate"
ENV PYTHONUNBUFFERED=1

# Create data directories
RUN mkdir -p /app/data/logs /app/data/test_results /app/data/reports

EXPOSE 5001

CMD ["python3", "src/app.py"]
