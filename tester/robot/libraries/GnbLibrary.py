# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""Robot Framework keyword library — gNB State Machine."""

import os, sys

PROJECT_ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", ".."))
for p in (PROJECT_ROOT, os.path.join(PROJECT_ROOT, "libs")):
    if p not in sys.path:
        sys.path.insert(0, p)

from robot.api.deco import keyword, library
from robot.api import logger
from src.statemachine.gnb_fsm import GnbStateMachine
from src.config import GNB_DEFAULTS, GNB_PROFILES_PATH, AMF_IP, AMF_PORT
from src.protocol.gnb_config import gnb_cfg_get


@library(scope='GLOBAL', version='1.0')
class GnbLibrary:
    ROBOT_LIBRARY_SCOPE = 'GLOBAL'

    def __init__(self):
        self._gnbs = {}

    @keyword("Create gNB")
    def create_gnb(self, name=None, amf_ip="127.0.0.1", amf_port=38412, mcc=None, mnc=None, tac=None):
        gnb = GnbStateMachine(amf_ip=amf_ip, amf_port=int(amf_port),
                               gnb_name=name, mcc=mcc, mnc=mnc, tac=tac)
        self._gnbs[gnb.gnb_name] = gnb
        logger.info(f"Created gNB: {gnb.gnb_name}")
        return gnb.gnb_name

    @keyword("Create gNB From Config")
    def create_gnb_from_config(self, config_name, **overrides):
        """Create a gNB from a saved config profile. Overrides (mcc, mnc, tac, etc.) are applied on top."""
        profile = gnb_cfg_get(GNB_PROFILES_PATH, config_name)
        if not profile:
            raise AssertionError(f"gNB config profile not found: {config_name}")
        gnb_id_str = str(profile.get("gnb_id", "0x500000"))
        gnb_id = int(gnb_id_str, 16) if gnb_id_str.startswith("0x") else int(gnb_id_str)
        slices = profile.get("slices", GNB_DEFAULTS["slices"])
        for s in slices:
            if isinstance(s.get("sd"), str) and s["sd"].startswith("0x"):
                s["sd"] = int(s["sd"], 16)
        gnb = GnbStateMachine(
            amf_ip=overrides.get("amf_ip", profile.get("amf_ip", AMF_IP)),
            amf_port=int(overrides.get("amf_port", profile.get("amf_port", AMF_PORT))),
            gnb_id=gnb_id,
            gnb_name=profile.get("gnb_name", config_name),
            mcc=overrides.get("mcc", profile.get("mcc", GNB_DEFAULTS["mcc"])),
            mnc=overrides.get("mnc", profile.get("mnc", GNB_DEFAULTS["mnc"])),
            tac=overrides.get("tac", profile.get("tac", GNB_DEFAULTS["tac"])),
            slices=slices,
            source_ip=profile.get("gnb_ip"),
        )
        self._gnbs[gnb.gnb_name] = gnb
        logger.info(f"Created gNB '{gnb.gnb_name}' from config profile '{config_name}'")
        return gnb.gnb_name

    @keyword("Connect SCTP Only")
    def connect_sctp_only(self, name):
        """Connect SCTP without sending NG Setup Request."""
        gnb = self._get(name)
        gnb.gnb_ip = gnb._sctp.connect(gnb.amf_ip, gnb.amf_port, source_ip=gnb.source_ip)
        logger.info(f"SCTP connected to {gnb.amf_ip}:{gnb.amf_port} (no NG Setup)")

    @keyword("Is SCTP Connected")
    def is_sctp_connected(self, name):
        return self._get(name)._sctp.connected

    @keyword("Connect gNB")
    def connect_gnb(self, name):
        if not self._get(name).connect():
            raise AssertionError(f"gNB {name} connect failed")

    @keyword("Wait gNB Ready")
    def wait_gnb_ready(self, name, timeout=10):
        if not self._get(name).wait_for_state("READY", int(timeout)):
            raise AssertionError(f"gNB {name} not READY (state={self._get(name).state})")

    @keyword("Connect gNB And Wait Ready")
    def connect_and_wait(self, name, timeout=10):
        self.connect_gnb(name)
        self.wait_gnb_ready(name, timeout)

    @keyword("Disconnect gNB")
    def disconnect_gnb(self, name):
        self._get(name).disconnect()

    @keyword("Get gNB State")
    def get_gnb_state(self, name):
        return self._get(name).state

    @keyword("gNB Should Be Ready")
    def gnb_should_be_ready(self, name):
        s = self._get(name).state
        if s != "READY": raise AssertionError(f"Expected READY, got {s}")

    @keyword("Get gNB UE Count")
    def get_gnb_ue_count(self, name):
        return len(self._get(name).ue_map)

    @keyword("Get All gNBs")
    def get_all_gnbs(self):
        return list(self._gnbs.keys())

    @keyword("Remove gNB")
    def remove_gnb(self, name):
        g = self._gnbs.pop(name, None)
        if g: g.disconnect()

    @keyword("Remove All gNBs")
    def remove_all_gnbs(self):
        for g in self._gnbs.values(): g.disconnect()
        self._gnbs.clear()

    def get_gnb_instance(self, name):
        return self._get(name)

    def _get(self, name):
        g = self._gnbs.get(name)
        if not g: raise AssertionError(f"gNB not found: {name}")
        return g
