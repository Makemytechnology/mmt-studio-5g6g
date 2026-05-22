# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""Robot Framework keyword library — UE State Machine."""

import os, sys, time

PROJECT_ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", ".."))
for p in (PROJECT_ROOT, os.path.join(PROJECT_ROOT, "libs")):
    if p not in sys.path:
        sys.path.insert(0, p)

from robot.api.deco import keyword, library
from robot.api import logger
from src.statemachine.ue_fsm import UeStateMachine
from src.protocol.sim_db import load_sim, load_all_sims, load_sims_auto
from src.config import SIM_DB_PATH


@library(scope='GLOBAL', version='1.0')
class UeLibrary:
    ROBOT_LIBRARY_SCOPE = 'GLOBAL'

    def __init__(self):
        self._ues = {}
        self._gnb_lib = None

    def _gnb(self):
        if not self._gnb_lib:
            from robot.libraries.BuiltIn import BuiltIn
            self._gnb_lib = BuiltIn().get_library_instance('GnbLibrary')
        return self._gnb_lib

    @keyword("Create UE From SIM DB")
    def create_ue_from_sim_db(self, imsi, db_path=None):
        sim = load_sim(imsi, db_path or SIM_DB_PATH)
        if not sim: raise AssertionError(f"SIM not found: {imsi}")
        self._ues[imsi] = UeStateMachine(sim)
        return imsi

    @keyword("Create All UEs From SIM DB")
    def create_all_ues(self, db_path=None):
        # json_path was removed when sim_db.json went away — UE list is
        # now derived in-memory from baseline.yaml on first access.
        sims = load_sims_auto(db_path or SIM_DB_PATH)
        created = []
        for sim in sims:
            if sim.imsi not in self._ues:
                self._ues[sim.imsi] = UeStateMachine(sim)
                created.append(sim.imsi)
        logger.info(f"Created {len(created)} UEs")
        return created

    @keyword("Load UEs From Config")
    def load_ues_from_config(self):
        """Load all UEs from the in-memory SIM list (lazy-init from
        baseline.yaml + any operator GUI deltas). Does not create — only
        reads."""
        sims = load_sims_auto(SIM_DB_PATH)
        if not sims:
            raise AssertionError("No UEs found in UE config database. Add UE entries first.")
        loaded = []
        for sim in sims:
            if sim.imsi not in self._ues:
                self._ues[sim.imsi] = UeStateMachine(sim)
                loaded.append(sim.imsi)
        logger.info(f"Loaded {len(loaded)} UE(s) from config: {loaded}")
        return loaded

    @keyword("Attach UE To gNB")
    def attach_ue_to_gnb(self, imsi, gnb_name):
        ue = self._get(imsi)
        gnb = self._gnb().get_gnb_instance(gnb_name)
        gnb.attach_ue(ue)

    @keyword("Register UE")
    def register_ue(self, imsi):
        if not self._get(imsi).register():
            raise AssertionError(f"UE {imsi} register failed")

    @keyword("Wait UE Registered")
    def wait_ue_registered(self, imsi, timeout=15):
        ue = self._get(imsi)
        if not ue.wait_for_state("REGISTERED", int(timeout)):
            raise AssertionError(f"UE {imsi} not REGISTERED (state={ue.state})")

    @keyword("Register UE And Wait")
    def register_and_wait(self, imsi, gnb_name=None, timeout=15):
        if gnb_name: self.attach_ue_to_gnb(imsi, gnb_name)
        self.register_ue(imsi)
        self.wait_ue_registered(imsi, timeout)

    @keyword("Deregister UE")
    def deregister_ue(self, imsi):
        self._get(imsi).deregister()

    @keyword("Wait UE Deregistered")
    def wait_ue_deregistered(self, imsi, timeout=15):
        ue = self._get(imsi)
        if not ue.wait_for_state("DEREGISTERED", int(timeout)):
            raise AssertionError(f"UE {imsi} not DEREGISTERED (state={ue.state})")

    @keyword("Deregister UE And Wait")
    def deregister_and_wait(self, imsi, timeout=15):
        self.deregister_ue(imsi)
        self.wait_ue_deregistered(imsi, timeout)

    @keyword("Establish PDU Session")
    def establish_pdu_session(self, imsi, dnn="internet", psi=1, sst=1, sd=None):
        sd_int = int(sd, 16) if isinstance(sd, str) and sd else (int(sd) if sd else None)
        if not self._get(imsi).establish_pdu_session(dnn=dnn, sst=int(sst), sd=sd_int, pdu_session_id=int(psi)):
            raise AssertionError(f"PDU session request failed")

    @keyword("Wait PDU Session Active")
    def wait_pdu_session_active(self, imsi, psi=1, timeout=15):
        ue, psi = self._get(imsi), int(psi)
        deadline = time.time() + int(timeout)
        while time.time() < deadline:
            if psi in ue.pdu_sessions:
                return ue.pdu_sessions[psi].get("ip", "unknown")
            time.sleep(0.5)
        raise AssertionError(f"PDU session {psi} not established")

    @keyword("Establish PDU Session And Wait")
    def establish_pdu_and_wait(self, imsi, dnn="internet", psi=1, sst=1, sd=None, timeout=15):
        self.establish_pdu_session(imsi, dnn, psi, sst, sd)
        return self.wait_pdu_session_active(imsi, psi, timeout)

    @keyword("Get UE State")
    def get_ue_state(self, imsi): return self._get(imsi).state

    @keyword("UE Should Be Registered")
    def ue_should_be_registered(self, imsi):
        s = self._get(imsi).state
        if s != "REGISTERED": raise AssertionError(f"Expected REGISTERED, got {s}")

    @keyword("UE Should Be Deregistered")
    def ue_should_be_deregistered(self, imsi):
        s = self._get(imsi).state
        if s != "DEREGISTERED": raise AssertionError(f"Expected DEREGISTERED, got {s}")

    @keyword("Get UE PDU Session IP")
    def get_ue_pdu_session_ip(self, imsi, psi=1):
        sess = self._get(imsi).pdu_sessions.get(int(psi))
        if not sess: raise AssertionError(f"No PDU session {psi}")
        return sess.get("ip", "unknown")

    @keyword("Get UE Security Algorithms")
    def get_ue_security_algorithms(self, imsi):
        ctx = self._get(imsi).security_ctx
        return {"eea": ctx.get("eea", 0), "eia": ctx.get("eia", 0)}

    @keyword("UE Should Have Security Keys")
    def ue_should_have_keys(self, imsi):
        if not self._get(imsi).security_ctx.get("knasint"):
            raise AssertionError(f"UE {imsi} has no NAS keys")

    @keyword("Get All UEs")
    def get_all_ues(self): return list(self._ues.keys())

    @keyword("Remove UE")
    def remove_ue(self, imsi): self._ues.pop(imsi, None)

    @keyword("Remove All UEs")
    def remove_all_ues(self): self._ues.clear()

    def _get(self, imsi):
        ue = self._ues.get(imsi)
        if not ue: raise AssertionError(f"UE not found: {imsi}")
        return ue
