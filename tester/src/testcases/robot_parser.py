# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""Parse Robot Framework .robot files to extract test case metadata.

Lightweight regex-based parser — no Robot Framework dependency required.
Extracts: tc_id, robot_name, suite, documentation, tags from each test case.
"""

import os
import re
import logging

log = logging.getLogger("tester.robot_parser")

# TC-ID pattern: TC-XXX-NNN at start of test name. Middle segment
# allows digits because some 3GPP terms include them (V2X, N26, P2P,
# 3GPP, …) and "[A-Z]+" alone would skip TC-V2X-001 → snake-case the
# whole name. Mirrors the spec.py _TC_ID_RE so the Python and robot
# sides agree on the tc_id shape.
_TC_ID_RE = re.compile(r'^(TC-[A-Z][A-Z0-9]*-\d+)\s+(.*)')


def _to_snake(name):
    """Convert 'Single UE Registration' → 'single_ue_registration'."""
    return re.sub(r'[\s\-]+', '_', name.strip()).lower()


def parse_robot_suite(robot_file_path):
    """Parse a single .robot file and extract test case metadata.

    Returns list of dicts, each with keys:
        tc_id, robot_name, suite, documentation, tags
    """
    with open(robot_file_path, "r", encoding="utf-8") as f:
        lines = f.readlines()

    suite_name = os.path.splitext(os.path.basename(robot_file_path))[0]
    # Extract suite-level documentation and tags
    suite_doc = ""
    suite_tags = []

    test_cases = []
    in_test_cases = False
    in_settings = False
    current_tc = None

    for raw_line in lines:
        line = raw_line.rstrip('\n\r')

        # Detect section headers
        if line.startswith('*** '):
            section = line.strip('* ').strip()
            in_test_cases = section.lower() in ('test cases', 'test case')
            in_settings = section.lower() in ('settings',)
            if not in_test_cases and current_tc:
                test_cases.append(current_tc)
                current_tc = None
            continue

        # Parse Settings section for suite-level doc and tags
        if in_settings:
            stripped = line.strip()
            if stripped.startswith('Documentation'):
                suite_doc = stripped.split(None, 1)[1] if len(stripped.split(None, 1)) > 1 else ""
            elif stripped.startswith('...') and suite_doc:
                continuation = stripped[3:].strip()
                suite_doc += " " + continuation
            elif stripped.startswith('Test Tags'):
                suite_tags = stripped.split()[2:]  # skip 'Test' and 'Tags'
            continue

        if not in_test_cases:
            continue

        # In test cases section
        # Non-indented, non-empty line = new test case
        if line and not line[0].isspace() and not line.startswith('#'):
            if current_tc:
                test_cases.append(current_tc)

            tc_name = line.strip()
            m = _TC_ID_RE.match(tc_name)
            if m:
                tc_id = m.group(1)
                short_name = m.group(2).strip()
            else:
                tc_id = _to_snake(tc_name)
                short_name = tc_name

            current_tc = {
                "tc_id": tc_id,
                "robot_name": tc_name,
                "short_name": short_name,
                "suite": suite_name,
                "documentation": "",
                "tags": list(suite_tags),  # inherit suite tags
                "_in_doc": False,
            }
            continue

        if current_tc is None:
            continue

        stripped = line.strip()

        # Comment line inside test case
        if stripped.startswith('#'):
            continue

        # [Documentation] tag
        if stripped.startswith('[Documentation]'):
            doc_text = stripped.split(']', 1)[1].strip() if ']' in stripped else ""
            current_tc["documentation"] = doc_text
            current_tc["_in_doc"] = True
            continue

        # Continuation line for documentation
        if current_tc["_in_doc"] and stripped.startswith('...'):
            continuation = stripped[3:].strip()
            if current_tc["documentation"]:
                current_tc["documentation"] += "\n" + continuation
            else:
                current_tc["documentation"] = continuation
            continue
        else:
            current_tc["_in_doc"] = False

        # [Tags] tag
        if stripped.startswith('[Tags]'):
            tag_text = stripped.split(']', 1)[1].strip() if ']' in stripped else ""
            tc_tags = [t.strip() for t in tag_text.split() if t.strip()]
            current_tc["tags"] = list(set(current_tc["tags"] + tc_tags))
            continue

    # Last test case
    if current_tc:
        test_cases.append(current_tc)

    # Clean up internal keys
    for tc in test_cases:
        tc.pop("_in_doc", None)

    return test_cases


def parse_all_suites(robot_dir):
    """Parse all .robot files under a directory tree.

    Walks subdirectories so the suites/ tree can be grouped (access/, session/, ...).
    Suites are sorted by relative path to keep ordering stable.

    Returns:
        list[dict] — all test cases from all suites, ordered by file then position
    """
    all_tcs = []
    if not os.path.isdir(robot_dir):
        log.warning("Robot suite directory not found: %s", robot_dir)
        return all_tcs

    robot_files = []
    for root, _dirs, files in os.walk(robot_dir):
        for f in files:
            if f.endswith('.robot'):
                full = os.path.join(root, f)
                robot_files.append((os.path.relpath(full, robot_dir), full))
    robot_files.sort()

    for rel, path in robot_files:
        try:
            tcs = parse_robot_suite(path)
            all_tcs.extend(tcs)
            log.info("Parsed %s: %d test cases", rel, len(tcs))
        except Exception as e:
            log.error("Failed to parse %s: %s", rel, e)

    log.info("Total robot test cases: %d from %d suites", len(all_tcs), len(robot_files))
    return all_tcs


# ── Suite name → human-readable category mapping ──
SUITE_CATEGORIES = {
    "01_registration": "Registration / NAS (TS 24.501)",
    "02_pdu_session": "PDU Session (TS 24.501 §8.3)",
    "04_stress": "Stress / Batch",
    "05_ng_setup": "NG Setup (TS 38.413 §8.7.1)",
    "06_authentication": "Authentication / 5G-AKA (TS 33.501)",
    "07_traffic": "Traffic / QoS (TS 23.501 §5.7)",
    "08_ims": "IMS / VoNR (TS 23.228)",
    "09_multi_traffic": "Multi-UE Traffic (Scale)",
    "10_ims_scale": "IMS / VoNR / ViNR (Scale)",
    "11_multi_dnn": "Multi-DNN (TS 23.501 §5.6.1)",
    "12_handover": "Handover (TS 38.413 §8.4)",
    "13_jumbo_frames": "Jumbo Frames (TS 29.281)",
    "14_release": "Release / RLF (TS 38.413 §8.3)",
    "15_idle_mode": "Idle Mode / Paging (TS 24.501)",
    "16_slicing": "Slicing (TS 23.501 §5.15)",
    "17_positioning": "Positioning (TS 23.273)",
    "18_iot": "IoT (TS 22.369 / TS 23.401)",
    "19_ntn": "NTN (TS 38.821)",
    "20_v2x": "V2X (TS 23.287)",
    "21_mcx": "MCX (TS 23.280)",
    "22_charging": "Charging (TS 32.290 / TS 32.291)",
    "23_dpi": "DPI (TS 29.244 §6.2.5)",
    "24_emergency": "Emergency (TS 23.501 §5.16.4)",
    "25_lawful_intercept": "Lawful Intercept (TS 33.127)",
    "26_esim": "eSIM (SGP.22)",
    "27_mec": "MEC (TS 23.548)",
    "28_nwdaf": "NWDAF (TS 23.288)",
    "29_roaming": "Roaming (TS 23.501 §5.6.3)",
    "30_trace": "OAM (TS 32.421 / TS 32.422)",
    "31_tcs_sidelink": "ProSe / Sidelink (TS 23.304)",
}


def suite_to_category(suite_name):
    """Map suite filename (without .robot) to a display category."""
    return SUITE_CATEGORIES.get(suite_name, suite_name.replace("_", " ").title())
