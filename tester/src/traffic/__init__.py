# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
# src/traffic/ — Traffic generation and reception engine
#
# Provides a clean functional interface for test cases:
#   engine = TrafficEngine.get()
#   session = engine.create_session(src_ip, dst_ip, ...)
#   session.start()
#   stats = session.stop()

from src.traffic.engine import TrafficEngine
from src.traffic.interface import TrafficSession, TrafficStats
