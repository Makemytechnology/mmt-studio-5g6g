# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
*** Settings ***
Documentation    eSIM (Embedded SIM) Test Suite
...              GSMA SGP.22 — RSP Technical Specification (Consumer)
...              GSMA SGP.32 — RSP Technical Specification (IoT / M2M)
...              Covers: eUICC profile download, enable/disable, profile list,
...              ICCID counter management, eUICC notification handling
Resource         ../../resources/common.resource
Test Setup       Setup Test Environment
Test Teardown    Teardown Test Environment
Test Tags        esim    euicc    rsp

*** Test Cases ***
# ===============================================================
# eSIM Profile Download & Management
# SGP.22 §3.1 — Profile Download and Installation
# SGP.22 §3.2 — Profile Enable / Disable
# ===============================================================
TC-ESIM-001 eUICC Profile Download
    [Documentation]    TC-ESIM-001: Download and install eSIM profile to eUICC
    ...    Standard: SGP.22 §3.1.3 (Profile Download procedure)
    ...    Procedure:
    ...    1. Prepare profile package on SM-DP+ (activation code generated)
    ...    2. eUICC initiates profile download via ES9+ interface
    ...    3. Mutual authentication between eUICC and SM-DP+
    ...    4. Profile bound to target EID, encrypted and downloaded
    ...    5. Profile installed and ICCID assigned
    ...    Verification:
    ...    - Profile state is "installed" (not yet enabled)
    ...    - ICCID and IMSI correctly provisioned
    ...    - Profile metadata (MNO name, icon) stored on eUICC
    ...    Expected Result: Profile downloaded and installed on eUICC
    [Tags]    download    install    smdp    priority-1
    Log    TC-ESIM-001: eUICC Profile Download

TC-ESIM-002 Profile Enable Disable
    [Documentation]    TC-ESIM-002: Enable and disable eSIM profiles on eUICC
    ...    Standard: SGP.22 §3.2 (Profile Enable/Disable)
    ...    Procedure:
    ...    1. eUICC has two installed profiles (Profile-A enabled, Profile-B disabled)
    ...    2. Disable Profile-A via LPA (Local Profile Assistant)
    ...    3. Enable Profile-B via LPA
    ...    4. eUICC performs profile switch (modem reset)
    ...    5. UE registers with Profile-B credentials (new IMSI)
    ...    Verification:
    ...    - Profile-A state transitions to "disabled"
    ...    - Profile-B state transitions to "enabled"
    ...    - Only one profile enabled at a time
    ...    - UE successfully registers with new profile IMSI
    ...    Expected Result: Profile switched, UE registers with new identity
    [Tags]    enable    disable    switch    priority-1
    Log    TC-ESIM-002: Profile Enable/Disable

TC-ESIM-003 Profile List Via REST API
    [Documentation]    TC-ESIM-003: List installed eSIM profiles via REST API
    ...    Standard: SGP.22 §3.3 (Profile inventory)
    ...    Procedure:
    ...    1. GET /api/esim/profiles?eid={eid}
    ...    2. Verify response contains all installed profiles
    ...    3. Each profile entry includes: ICCID, IMSI, state (enabled/disabled),
    ...       MNO-OID, profile_class (test/provisioning/operational)
    ...    Verification:
    ...    - Profile list matches installed profiles on eUICC
    ...    - Exactly one profile in "enabled" state
    ...    - ICCID format valid (19-20 digits, Luhn check)
    ...    Expected Result: Complete profile inventory returned
    [Tags]    api    profile-list    inventory    priority-1
    Log    TC-ESIM-003: Profile List via REST API

# ===============================================================
# ICCID & Counter Management
# SGP.22 §2.5.1 — ICCID allocation
# SGP.32 §4.1 — IoT profile provisioning counters
# ===============================================================
TC-ESIM-010 ICCID Counter Management
    [Documentation]    TC-ESIM-010: Verify ICCID allocation counter increments correctly
    ...    Standard: SGP.22 §2.5.1 (ICCID structure and allocation)
    ...    Procedure:
    ...    1. GET /api/esim/iccid-counter to read current counter value
    ...    2. Download a new profile (TC-ESIM-001 flow)
    ...    3. GET /api/esim/iccid-counter again
    ...    4. Verify counter incremented by 1
    ...    5. Verify new ICCID follows sequential allocation (no gaps)
    ...    Verification:
    ...    - Counter value increases monotonically
    ...    - ICCID Luhn check digit correct
    ...    - No duplicate ICCIDs in profile inventory
    ...    Expected Result: ICCID counter managed correctly
    [Tags]    iccid    counter    allocation    priority-2
    Log    TC-ESIM-010: ICCID Counter Management

TC-ESIM-011 eUICC Notification Handling
    [Documentation]    TC-ESIM-011: Verify eUICC notifications delivered to SM-DP+
    ...    Standard: SGP.22 §3.5 (Notification management)
    ...    Procedure:
    ...    1. eUICC performs profile operation (download/enable/disable/delete)
    ...    2. eUICC generates notification with:
    ...       - sequenceNumber, profileManagementOperation
    ...       - notificationAddress (SM-DP+ URL)
    ...    3. Notification sent to SM-DP+ via ES9+ HandleNotification
    ...    4. SM-DP+ acknowledges receipt
    ...    Verification:
    ...    - Notification generated for each profile operation
    ...    - Notification contains correct operation type
    ...    - SM-DP+ processes and acknowledges notification
    ...    - Pending notification list empty after delivery
    ...    Expected Result: All eUICC notifications delivered and acknowledged
    [Tags]    notification    smdp    es9    priority-2
    Log    TC-ESIM-011: eUICC Notification Handling
