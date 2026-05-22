"""Allow running: python -m sa_tester"""
import os, sys

PROJECT_ROOT = os.path.dirname(os.path.abspath(__file__))
for p in (PROJECT_ROOT, os.path.join(PROJECT_ROOT, "libs"),
          os.path.join(PROJECT_ROOT, "libs", "pycrate")):
    if p not in sys.path:
        sys.path.insert(0, p)

from src.app import app
from src.config import AMF_IP, AMF_PORT, TESTER_WEB_PORT

import argparse
parser = argparse.ArgumentParser(description="SA Tester — 5G Core Network Tester")
parser.add_argument("--amf-ip", default=AMF_IP)
parser.add_argument("--amf-port", type=int, default=AMF_PORT)
parser.add_argument("--port", type=int, default=TESTER_WEB_PORT)
parser.add_argument("--sim-db", default="", help="Path to core's sacore.db")
parser.add_argument("--auto-setup", action="store_true")
args = parser.parse_args()

import src.config as cfg
cfg.AMF_IP = args.amf_ip
cfg.AMF_PORT = args.amf_port
if args.sim_db:
    cfg.SIM_DB_PATH = args.sim_db

if args.auto_setup:
    from src.statemachine import GnbStateMachine, UeStateMachine
    from src.protocol.sim_db import load_sims_auto
    from src.app import gnb_pool, ue_pool
    gnb = GnbStateMachine(amf_ip=args.amf_ip, amf_port=args.amf_port)
    gnb_pool.append(gnb)
    gnb.connect()
    for sim in load_sims_auto(cfg.SIM_DB_PATH):
        ue_pool.append(UeStateMachine(sim))

import logging
log = logging.getLogger("sa_tester")
log.info("SA Tester on port %d → AMF %s:%d", args.port, args.amf_ip, args.amf_port)
app.run(host="0.0.0.0", port=args.port, debug=False, use_reloader=False)
