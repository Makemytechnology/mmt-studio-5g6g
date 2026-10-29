# ims — IP Multimedia Subsystem

## 1. Role / scope

`services/ims/` implements the IMS application + media plane: a
combined P/I/S-CSCF (per TS 24.229 §5.4.1 — only S-CSCF
registration FSM is wired today), an HSS-mirror data store
(TS 23.228 §4.3.3 + §5.2 stored profile), an MMTel/TAS slice
(TS 24.604 et al. via `services/supplementary/`), a Conference
Application Server (TS 24.147 + RFC 4575 / 4579), an SDP / RTP relay
(RFC 4566 / 3264 / 3550), and the MRFP audio-mixer + video-compositor
(TS 23.228 §4.7).

The package is split per IMS functional entity:

| Sub-package | Spec entity | Wire surface |
|-------------|-------------|--------------|
| `ims/cscf/` | P/I/S-CSCF (TS 24.229 §5.4) | SIP REGISTER (parse + auth) |
| `ims/` (hss.go) | HSS / Cx data view (TS 23.228 §4.3.3, TS 29.228) | Cx UAR / SAR / LIR helpers |
| `ims/conference/` | Conference AS focus (TS 24.147 §5.3.2) | Conference URI factory + RFC 4575 XML |
| `ims/media/` | SDP, RTP relay (RFC 4566 / 3264 / 3550) | UDP RTP relay sockets |
| `ims/mrfp/` | MRFP (TS 23.228 §4.7) | RTP recv/send + audio mixer + video forwarder |

## 2. Architecture

```
                         ┌────────────────────────────────────────────┐
                         │                  UE                        │
                         └────────────┬───────────────────────────────┘
                              SIP ↕                       │ RTP ↕
                                     │                    │
        ┌────────────────────────────┴───────┐    ┌───────┴────────────┐
        │            services/ims/cscf/      │    │  services/ims/     │
        │                                    │    │   media/           │
        │  CSCF{registrations, dialogs}      │    │                    │
        │  ├── HandleRegister(req, cfg)      │    │  RtpRelay (UDP↔UDP)│
        │  ├── ParseRegister                 │    │  SDP parse / build │
        │  └── per-IMPI Registration FSM     │    │  NegotiateCodecs   │
        │                                    │    │                    │
        │  states: NotRegistered →           │    │  Session (offer/   │
        │          Challenged →              │    │   answer/active/   │
        │          Registered                │    │   released)        │
        │                                    │    └───────┬────────────┘
        │  transitions:                      │            │
        │    unprotected→401 challenge       │   to MRFP for conferences
        │    protected   →200 OK / 403       │            ▼
        │    Expires=0   →200 OK + dereg     │    ┌────────────────────┐
        └─┬──────────────────────────────────┘    │ services/ims/mrfp/ │
          │                                       │                    │
          │ Cx (TS 29.228)                        │  Per-conference    │
          │  UAR/SAR/LIR helpers (Diameter        │   AudioMixer +     │
          │   codes 2001/2002/5001/5003)          │   VideoCompositor  │
          ▼                                       │                    │
   ┌──────────────────────┐                       │  ParticipantRtp ×N │
   │  services/ims/       │                       │   (RTP recv/send)  │
   │   (hss.go)           │                       │                    │
   │                      │                       │  Active speaker    │
   │  ims_subscribers     │                       │  detection + L16   │
   │  ims_service_profile │                       │  mixing + H264     │
   │                      │                       │  forwarding        │
   │  IMS regMap          │                       └────────────────────┘
   │  EvaluateIFCs        │
   │   (returns           │   ┌────────────────────────────────────────┐
   │   sip:mmtel@tas      │   │   services/ims/conference/             │
   │   for INVITE)        │   │                                        │
   └──────────┬───────────┘   │  ConferenceAS{conferences}             │
              │               │  CreateConference / Join / Leave / End │
              │ via iFC route │  IsConferenceURI                        │
              ▼               │  BuildConferenceEventXML (RFC 4575)    │
   ┌──────────────────────┐   └────────────────────────────────────────┘
   │ services/supplementary
   │  (MMTel — CDIV/CW/   │
   │   CB/OIP/OIR/TIP/TIR)│
   └──────────────────────┘
```

## 3. File map

| File | LOC | Role |
|------|-----|------|
| `hss.go` | 284 | Subscriber CRUD + IMS regMap + Cx UAR/SAR/LIR; `EvaluateIFCs` returns the MMTel TAS hop for `INVITE` |
| `cscf/cscf.go` | 137 | CSCF container; per-IMPI Registration map; dialog map |
| `cscf/handler.go` | 226 | `HandleRegister` — wire to `Registration` FSM, builds SIP response |
| `cscf/register.go` | 109 | `ParseRegister` + `ExtractIMPI` / `ExtractIMPU` / `IMSIFromIMPI` |
| `cscf/registration.go` | 348 | Per-IMPI Registration FSM (3 states, 7 events, 2 timers) |
| `conference/conference.go` | 178 | Conference factory + RFC 4575 XML generator |
| `media/media.go` | 389 | SDP parse / build / negotiate; per-call media session; UDP RTP relay |
| `mrfp/mrfp.go` | 294 | RTP I/O, AudioMixer (PCM L16 mix), VideoCompositor (active-speaker forward), MRFP top-level |

Test files: `cscf/handler_test.go`, `cscf/register_test.go`,
`cscf/registration_test.go`, `conference/conference_test.go`,
`media/media_test.go`, `mrfp/mrfp_test.go`. Total ~ 2.9k LOC.

## 4. Wire / API surface

### SIP (TS 24.229 §5.4)

`HandleRegister(req, RegisterHandlerConfig)` (`cscf/handler.go:51-97`)
is the SIP REGISTER pathway:

```
ParseRegister(req) → RegisterFields{IMPI, IMPU, Contact, Expires}
   ↓
   Expires == 0?           → reg.OnDeregister()         [§5.4.1.4]
   integrity-protected=yes? → reg.OnProtectedRegister() [§5.4.1.2.2]
   else                    → reg.OnUnprotectedRegister([§5.4.1.2.1]
                              with cfg.GenerateAV(impi))
   ↓ RegResult{Code, Reason, Challenge?}
   ↓
buildResponse(req, code, reason, extraHeaders) — echoes
   Via/From/To/Call-ID/CSeq per RFC 3261 §8.2.6
   ↓
   401 → WWW-Authenticate (encodeAKAChallenge, §5.4.1.2.1A)
   200 + Expires>0 → Expires header
```

The AKA primitives (`GenerateAV`, `VerifyAuth`) are caller-supplied
callbacks (`RegisterHandlerConfig`). RES/XRES comparison
(TS 33.203, §5.4.1.2.2) and Digest AKAv1-MD5 / AKAv2-SHA-256
encoding (RFC 3310 / 4169) are NOT done in this package.

### Cx (TS 29.228 / TS 29.229)

In-process Diameter helpers (`hss.go:188-254`):

| Function | Purpose | Returns |
|----------|---------|---------|
| `UserAuthorizationRequest(impi, impu)` | UAR / UAA | (2001 first / 2002 subsequent / 5001 unknown, scscf URI) |
| `ServerAssignmentRequest(impi, impu, server, type)` | SAR / SAA — REGISTRATION sets `regMap` (3600 s expires); DEREGISTRATION clears it | (2001 / 5001) |
| `LocationInfoRequest(impu)` | LIR / LIA | (2001 / 5003 not registered) |

Diameter codes are defined as constants (`hss.go:188-195`):
`DiameterSuccess=2001`, `DiameterFirstRegistration=2001`,
`DiameterSubsequentRegistration=2002`, `DiameterErrorUserUnknown=5001`,
`DiameterErrorIdentityNotRegistered=5003`.

### MMTel iFC routing (TS 24.173 / TS 24.604)

`EvaluateIFCs(impu, sipMethod)` (`hss.go:279-284`) is a stub: returns
`["sip:mmtel@tas.local"]` for `INVITE`, `nil` otherwise. The full §5.2
Service Profile / Initial Filter Criteria fan-out is collapsed
(TODO at `hss.go:29-34`).

### SDP (RFC 4566 / 3264)

`media/media.go` parses + builds SDP, intersects offer with local
capabilities (`NegotiateCodecs`), and ships canonical IMS offers:

| Function | Notes |
|----------|-------|
| `ParseSDP(text)` / `BuildSDP(s)` | v=, o=, s=, c=, t=, m=, a= |
| `BuildVoiceSDP(ip, port, sid)` | AMR-WB (PT 96, `mode-set=0,1,2;octet-align=1`) + AMR-NB (PT 97) |
| `BuildVideoSDP(ip, ap, vp, sid)` | Voice + H264 (PT 98, `profile-level-id=42e01f`) |
| `NegotiateCodecs(offer, supported)` | Intersect formats; preserves rtpmap/fmtp lines for kept PTs and direction attributes |

Per-call `Session` map is keyed by `Call-ID` with states
`offering` → `active` → `released` (`media.go:54-117`).

### RTP relay (RFC 3550)

`RtpRelay{PortMin..PortMax}` allocates even ports and runs
two goroutines per session — one per direction — through
`(*RtpRelaySession).fwd` (`media.go:314-327`). Peer addresses are
auto-learned from the first received packet; subsequent UDP packets
are forwarded to the learned peer.

### Conference AS (TS 24.147 + RFC 4575)

`ConferenceAS{conferences}`:

| Function | Notes |
|----------|-------|
| `CreateConference(hostIMPU)` | Returns `Conference + sip:conf-N@domain` URI; host added as audio participant |
| `JoinConference(confID, impu, mediaType)` | Accepts `audio` (default) or `audio+video` |
| `LeaveConference(confID, impu)` | Conference auto-ends when last participant leaves |
| `EndConference(confID)` | State → `ended` |
| `IsConferenceURI(uri)` | Matches `sip:conf-` or `sip:conference-factory@` prefix |
| `BuildConferenceEventXML(c, uri)` | RFC 4575 `conference-info` body |

### MRFP (TS 23.228 §4.7)

Per-conference `MixerSession` with `AudioMixer + VideoCompositor` and
N `ParticipantRtpSession` entries (one per (participant, media-type)):

| Constant | Value | Notes |
|----------|-------|-------|
| `SampleRate` | 16000 Hz | |
| `FrameMS` / `FrameSamples` / `FrameBytes` | 20 ms / 320 / 640 | |
| `PayloadTypeL16` | 96 | Dynamic PT for the L16 mix output |
| `RTPHeaderSize` | 12 | RFC 3550 §5 |

Audio mix loop (`AudioMixer.loop`, `mrfp/mrfp.go:169-187`): every 20 ms,
decode each participant's latest payload to L16 PCM, compute energy,
mix all participants minus self into the per-recipient output,
encode back to L16, send. Active-speaker selection: the participant
with the highest RMS energy > 100 becomes `Active` and triggers
`OnSpkr` → `VideoCompositor.SetActive` so the video plane forwards
the active speaker's last received H264 frame to all others.

## 5. Headline procedures

### Registration FSM (TS 24.229 §5.4.1)

`Registration` (`cscf/registration.go`) is a libs/fsm machine with one
goroutine per IMPI. States and events:

```
                            ┌──────────────────┐
                            │ NotRegistered    │
                            └────┬─────────────┘
                                 │ unprotected REGISTER + cfg.GenerateAV
                                 │ → 401 + AV (cached)         [§5.4.1.2.1]
                                 │   timer:challenge=30s
                                 ▼
                            ┌──────────────────┐
                            │   Challenged     │
                            └────┬─────────────┘
            challenge      │     │ protected REGISTER (authOK)
            timeout        │     │ → 200 OK                     [§5.4.1.2.2]
            (drop AV)      │     │   timer:reg-expires=Expires*1s
                           ▼     ▼
                            ┌──────────────────┐
                ◀──────────┤ Registered       ├──── REGISTER Expires=0
                  reg-     └──────────────────┘     → 200 OK [§5.4.1.4]
                  expires       ▲   │
                  timeout       │   │ unprotected REGISTER (rereg)
                                │   ▼ → 401 + AV
                                ────  → Challenged

   Network-initiated dereg (any state) → NotRegistered  [§5.4.1.5]
```

`RegResult` returned to `HandleRegister`: `{Code int, Reason string,
Challenge map[string][]byte}`. `Code` ∈ {200, 401, 403, 423, 500}.
Auth-fail (`authOK=false`) returns 403 `auth_failed` per
TS 24.229 §5.4.1.2.3A and drops the cached AV.

### REGISTER → 401 → REGISTER → 200 round-trip

```
UE → S-CSCF: REGISTER (no Authorization)
  HandleRegister → ParseRegister → reg.OnUnprotectedRegister(impu, ...)
  cfg.GenerateAV(impi) → AV{rand, autn, xres, ck, ik, ...}
  reg state: NotRegistered → Challenged
  WWW-Authenticate: Digest realm="ims.example.com", k=<...>
S-CSCF → UE: 401 Unauthorized + WWW-Authenticate

UE → S-CSCF: REGISTER + Authorization: Digest username="...",
                                       integrity-protected=yes, ...
  HandleRegister → ParseRegister → reg.OnProtectedRegister(...)
  cfg.VerifyAuth(impi, authHdr) → true  (caller checks RES vs XRES)
  reg state: Challenged → Registered, reg-expires armed
S-CSCF → UE: 200 OK + Expires: 600
```

### Conference create + join

```
UE.A → AS: INVITE sip:conference-factory@ims.example.com
  AS: CreateConference(A) → c, sip:conf-1@ims.example.com
  c.Participants[A] = {audio}
AS → UE.A: 200 OK + Contact: sip:conf-1@ims.example.com

UE.B → AS: INVITE sip:conf-1@ims.example.com
  AS: JoinConference("conf-1", B, "audio")
AS → UE.B: 200 OK
AS → subscribers: NOTIFY conference-info (RFC 4575)
       — body via BuildConferenceEventXML(c, uri)
```

(The actual NOTIFY emission is a TODO — see §6.)

### MRFP audio mix

```
N participants × (RTP recv on local port → ParseRTPHeader → latest payload)
  ↓ every 20 ms
loop():
  for each pid: decode(latest, latestPT) → []int16
  for each pid: levels[pid] = energy(pcm)
  top, topE := argmax(levels)
  if topE > 100 && top != Active: Active = top; OnSpkr(top)
  for each pid: send(BuildRTPPacket(L16, mix(others-without-pid)))
```

VideoCompositor (`mrfp/mrfp.go:233-243`) forwards the `Active`
participant's latest video payload (with original PT) to all other
participants every ~1 ms.

## 6. Key types

```go
// services/ims/hss.go
type Subscriber  { ID, UEIMSI, MSISDN, IMPI, IMPU, ServiceProfileID *int64 }
type Registration { IMPI, IMPU, Contact, Expires int, Registered bool }
                   // in-memory (regMap)

// services/ims/cscf/cscf.go
type CSCF       { Config{Host, Port, IMSDomain, ServerName},
                  registrations map[IMPI]*Registration,
                  dialogs       map[CallID]*DialogInfo }
type DialogInfo { ToTag, CallerAddr, CalleeAddr }

// services/ims/cscf/registration.go
type Registration { IMPI, IMPU, Contact, Expires int, av map[string][]byte,
                    fsm *fsm.Machine }
type RegResult    { Code int, Reason string, Challenge map[string][]byte }
const StateNotRegistered / StateChallenged / StateRegistered

// services/ims/cscf/handler.go
type RegisterHandlerConfig {
    GenerateAV func(impi string) map[string][]byte
    VerifyAuth func(impi, authHdr string) bool
}

// services/ims/conference/conference.go
type Conference { ID, HostIMPU, Participants map[IMPU]*Participant,
                  CreatedAt time.Time, State string }
type Participant { IMPU, JoinedAt, MediaType, Muted }
type ConferenceAS { conferences map[ID]*Conference, domain, counter }

// services/ims/media/media.go
type Session    { CallID, FromURI, ToURI, MediaType, Codec, State,
                  SDPOffer, SDPAnswer }
type SdpSession { Version, Origin, SessionName, Connection, Timing,
                  Media []SdpMedia }
type SdpMedia   { MediaType, Port, Protocol, Formats, Attributes }
type RtpRelay   { PortMin, PortMax, sessions }
type RtpRelaySession { SessionID, LocalPortA, LocalPortB,
                       callerAddr, calleeAddr }

// services/ims/mrfp/mrfp.go
type ParticipantRtpSession { ID, Host, LocalPort, SSRC, ... }
type AudioMixer            { ConfID, parts, levels, Active, OnSpkr }
type VideoCompositor       { ConfID, parts, active }
type MixerSession          { ConfID, Audio, Video, Parts }
type MRFP                  { Host, AdvIP, PortMin, PortMax, sess }
```

## 7. Stubs / TODOs

| Location | Spec | Note |
|----------|------|------|
| `hss.go:29-34` | TS 23.228 §5.2 | iFC engine reduced to `EvaluateIFCs` returning a hard-coded MMTel hop for INVITE; full Initial Filter Criteria evaluation not built |
| `cscf/handler.go:84-91` | RFC 3310 / 4169 + TS 33.203 | `encodeAKAChallenge` is a thin AV-fields encoder; full Digest AKAv1-MD5 / AKAv2-SHA-256 encoding belongs in a TS 33.203 wrapper |
| `cscf/registration.go:34-41` | TS 33.203 | Auth mechanism dispatch (IMS-AKA / SIP Digest / NASS-IMS-bundled / GPRS-IMS-bundled) is one level up; only IMS-AKA is wired |
| `conference/conference.go:27-37` | TS 24.147 §5.3.2 / .3 | Focus role does not emit SIP REFER for participant invitation, INFO for floor mute control; conference-event SUBSCRIBE management not tracked |
| `media/media.go:26-30` | TS 24.229 §6.1.2 / RFC 3312 / 4032 | SDP precondition handling ("qos" tag); today every offer is treated as having preconditions satisfied |
| `media/media.go:32-35` | RFC 4566 §6 | Comprehensive a= attribute parsing — only codec/format-specific attributes used by the relay are extracted |
| `mrfp/mrfp.go:27-30` | RFC 3550 §6 / RFC 3611 | RTCP SR/RR + RTCP-XR not emitted |
| `mrfp/mrfp.go:31-34` | TS 23.228 §4.7 | MRFP-level codec transcoding (AMR ↔ G.711 ↔ Opus) — payload bytes forwarded verbatim |

## 8. References

All §-cites in source. Primary stack:

- **TS 23.003** §13.3 — IMS domain naming convention (used by
  `DefaultDomain`)
- **TS 23.228** §4.3.3 — IMPI / IMPU schemes
- **TS 23.228** §4.7 — MRFC / MRFP split
- **TS 23.228** §5.2 — Application-level registration (Stored
  Information / Service Profile)
- **TS 24.229** §5.4 / §5.4.1 — S-CSCF procedures (`§5.4.1.1`,
  `§5.4.1.2.1`, `§5.4.1.2.1A`, `§5.4.1.2.2`, `§5.4.1.2.2F`,
  `§5.4.1.2.3A`, `§5.4.1.4`, `§5.4.1.5`)
- **TS 24.229** §5.4.3 — Dialog handling (held in `dialogs` map)
- **TS 24.229** §6.1 — SDP general handling for IMS
- **TS 24.147** §5.2.3 / §5.3.2 / §5.3.3 — Conferencing AS, focus role,
  notification service
- **TS 29.228** — Cx (UAR/SAR/LIR Diameter codes)
- **RFC 3261** §8.2.6 — Response header echo
- **RFC 3264** — Offer/Answer
- **RFC 3550** §5 — RTP fixed header
- **RFC 3551** — Audio/video RTP profile (PT assignments)
- **RFC 4566** — SDP
- **RFC 4575** — `conference` event package XML
- **RFC 4579** — Call-control conferencing for UAs

---

*Last refreshed against commit `13a181d`.*
