// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package media — SDP parser/builder + RTP relay + session
// tracking.
//
// IETF anchors:
//
//   - RFC 4566          SDP (Session Description Protocol) — v=,
//                         o=, s=, t=, c=, m= and the per-media
//                         a= attribute lines that the parser /
//                         builder below emit and consume.
//   - RFC 3264          Offer/Answer model — the "offering" /
//                         "active" state-machine names below
//                         track the offer/answer lifecycle.
//   - RFC 3550          RTP — used by the relay path; full RTP
//                         header parsing lives in the mrfp/
//                         sub-package, not here.
//
// 3GPP anchors:
//
//   - TS 24.229 §6.1    SDP general handling for IMS — the
//                         offer/answer constraints (precondition
//                         model, codec list ordering) the IMS
//                         consumer of this package must obey.
//
// TODO(spec: TS 24.229 §6.1.2): SDP precondition handling per
// RFC 3312 / RFC 4032 (the "qos" precondition tag) is not built
// — IMS relies on this for resource reservation before the
// 200 OK is sent. Today this package treats every offer/answer
// as having the preconditions satisfied immediately.
//
// TODO(spec: RFC 4566 §6): comprehensive a= attribute parsing —
// only the codec / format-specific attributes the relay path
// needs are extracted; bandwidth (b=), key management (a=key-
// mgmt) and rtcp-mux are passed through verbatim.
package media

import (
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mmt/mmt-studio-core/oam/logger"
)

var log = logger.Get("ims.media")

// ── Media session tracking (existing) ──

// Session is an active media session between two endpoints.
type Session struct {
	CallID    string `json:"call_id"`
	FromURI   string `json:"from_uri"`
	ToURI     string `json:"to_uri"`
	MediaType string `json:"media_type"`
	Codec     string `json:"codec"`
	State     string `json:"state"`
	SDPOffer  string `json:"sdp_offer,omitempty"`
	SDPAnswer string `json:"sdp_answer,omitempty"`
}

var (
	mu       sync.RWMutex
	sessions = map[string]*Session{}
)

func Create(callID, from, to, mediaType, sdpOffer string) {
	mu.Lock()
	defer mu.Unlock()
	sessions[callID] = &Session{
		CallID: callID, FromURI: from, ToURI: to,
		MediaType: mediaType, SDPOffer: sdpOffer, State: "offering",
	}
}

func Accept(callID, codec, sdpAnswer string) {
	mu.Lock()
	defer mu.Unlock()
	if s, ok := sessions[callID]; ok {
		s.Codec = codec
		s.SDPAnswer = sdpAnswer
		s.State = "active"
	}
}

func Release(callID string) {
	mu.Lock()
	defer mu.Unlock()
	if s, ok := sessions[callID]; ok {
		s.State = "released"
	}
}

func List() []Session {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]Session, 0, len(sessions))
	for _, s := range sessions {
		out = append(out, *s)
	}
	return out
}

func ActiveCount() int {
	mu.RLock()
	defer mu.RUnlock()
	n := 0
	for _, s := range sessions {
		if s.State == "active" {
			n++
		}
	}
	return n
}

// ── SDP ──

// SdpMedia represents a single m= line with attributes.
type SdpMedia struct {
	MediaType  string   `json:"media_type"`
	Port       int      `json:"port"`
	Protocol   string   `json:"protocol"`
	Formats    []string `json:"formats"`
	Attributes []string `json:"attributes"`
}

// SdpSession is an SDP session description.
type SdpSession struct {
	Version     int        `json:"version"`
	Origin      string     `json:"origin"`
	SessionName string     `json:"session_name"`
	Connection  string     `json:"connection"`
	Timing      string     `json:"timing"`
	Media       []SdpMedia `json:"media"`
}

func NewSdpSession() *SdpSession {
	return &SdpSession{Origin: "- 0 0 IN IP4 0.0.0.0", SessionName: "-",
		Connection: "IN IP4 0.0.0.0", Timing: "0 0"}
}

func (s *SdpSession) GetMedia(mediaType string) *SdpMedia {
	for i := range s.Media {
		if s.Media[i].MediaType == mediaType {
			return &s.Media[i]
		}
	}
	return nil
}

// ParseSDP parses SDP text into an SdpSession.
func ParseSDP(text string) *SdpSession {
	sess := NewSdpSession()
	var cur *SdpMedia
	for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
		line = strings.TrimRight(strings.TrimSpace(line), "\r")
		if len(line) < 2 || line[1] != '=' {
			continue
		}
		field, value := line[0], line[2:]
		switch field {
		case 'v':
			sess.Version, _ = strconv.Atoi(value)
		case 'o':
			sess.Origin = value
		case 's':
			sess.SessionName = value
		case 'c':
			if cur == nil {
				sess.Connection = value
			}
		case 't':
			sess.Timing = value
		case 'm':
			parts := strings.Fields(value)
			if len(parts) >= 3 {
				port, _ := strconv.Atoi(parts[1])
				m := SdpMedia{MediaType: parts[0], Port: port, Protocol: parts[2], Formats: parts[3:]}
				sess.Media = append(sess.Media, m)
				cur = &sess.Media[len(sess.Media)-1]
			}
		case 'a':
			if cur != nil {
				cur.Attributes = append(cur.Attributes, value)
			}
		}
	}
	return sess
}

// BuildSDP renders SdpSession to wire format.
func BuildSDP(s *SdpSession) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "v=%d\r\n", s.Version)
	fmt.Fprintf(&sb, "o=%s\r\n", s.Origin)
	fmt.Fprintf(&sb, "s=%s\r\n", s.SessionName)
	fmt.Fprintf(&sb, "c=%s\r\n", s.Connection)
	fmt.Fprintf(&sb, "t=%s\r\n", s.Timing)
	for _, m := range s.Media {
		fmt.Fprintf(&sb, "m=%s %d %s %s\r\n", m.MediaType, m.Port, m.Protocol, strings.Join(m.Formats, " "))
		for _, a := range m.Attributes {
			fmt.Fprintf(&sb, "a=%s\r\n", a)
		}
	}
	return sb.String()
}

// BuildVoiceSDP creates a voice SDP offer with AMR-WB and AMR-NB.
func BuildVoiceSDP(ip string, port, sessionID int) *SdpSession {
	return &SdpSession{
		Origin: fmt.Sprintf("- %d %d IN IP4 %s", sessionID, sessionID, ip),
		Connection: fmt.Sprintf("IN IP4 %s", ip), Timing: "0 0",
		Media: []SdpMedia{{MediaType: "audio", Port: port, Protocol: "RTP/AVP",
			Formats: []string{"96", "97"}, Attributes: []string{
				"rtpmap:96 AMR-WB/16000", "fmtp:96 mode-set=0,1,2;octet-align=1",
				"rtpmap:97 AMR/8000", "fmtp:97 octet-align=1", "sendrecv"}}},
	}
}

// BuildVideoSDP creates a voice + video SDP offer.
func BuildVideoSDP(ip string, audioPort, videoPort, sessionID int) *SdpSession {
	s := BuildVoiceSDP(ip, audioPort, sessionID)
	s.Media = append(s.Media, SdpMedia{MediaType: "video", Port: videoPort, Protocol: "RTP/AVP",
		Formats: []string{"98"}, Attributes: []string{
			"rtpmap:98 H264/90000", "fmtp:98 profile-level-id=42e01f", "sendrecv"}})
	return s
}

// NegotiateCodecs intersects SDP offer with local capabilities.
func NegotiateCodecs(offer *SdpSession, supported map[string][]string) *SdpSession {
	if supported == nil {
		supported = map[string][]string{"audio": {"96", "97"}, "video": {"98"}}
	}
	answer := &SdpSession{Origin: offer.Origin, Connection: offer.Connection, Timing: "0 0"}
	for _, m := range offer.Media {
		sup := make(map[string]bool)
		for _, f := range supported[m.MediaType] {
			sup[f] = true
		}
		var common []string
		for _, f := range m.Formats {
			if sup[f] {
				common = append(common, f)
			}
		}
		if len(common) > 0 {
			cs := make(map[string]bool)
			for _, f := range common {
				cs[f] = true
			}
			var attrs []string
			for _, a := range m.Attributes {
				keep := false
				for f := range cs {
					if strings.HasPrefix(a, "rtpmap:"+f+" ") || strings.HasPrefix(a, "fmtp:"+f+" ") {
						keep = true
					}
				}
				if a == "sendrecv" || a == "sendonly" || a == "recvonly" || a == "inactive" {
					keep = true
				}
				if keep {
					attrs = append(attrs, a)
				}
			}
			answer.Media = append(answer.Media, SdpMedia{MediaType: m.MediaType, Port: m.Port,
				Protocol: m.Protocol, Formats: common, Attributes: attrs})
		}
	}
	return answer
}

// ── RTP Relay ──

// RtpRelaySession is a single RTP relay session.
type RtpRelaySession struct {
	SessionID  string
	LocalPortA int
	LocalPortB int
	callerAddr *net.UDPAddr
	calleeAddr *net.UDPAddr
	sockA, sockB *net.UDPConn
	running      bool
	smu          sync.Mutex
}

func (rs *RtpRelaySession) Start() error {
	var err error
	rs.sockA, err = net.ListenUDP("udp", &net.UDPAddr{Port: rs.LocalPortA})
	if err != nil {
		return err
	}
	rs.sockB, err = net.ListenUDP("udp", &net.UDPAddr{Port: rs.LocalPortB})
	if err != nil {
		rs.sockA.Close()
		return err
	}
	rs.running = true
	go rs.fwd(rs.sockA, rs.sockB, true)
	go rs.fwd(rs.sockB, rs.sockA, false)
	log.Infof("RTP session %s started: portA=%d portB=%d", rs.SessionID, rs.LocalPortA, rs.LocalPortB)
	return nil
}

func (rs *RtpRelaySession) Stop() {
	rs.running = false
	if rs.sockA != nil { rs.sockA.Close() }
	if rs.sockB != nil { rs.sockB.Close() }
}

func (rs *RtpRelaySession) fwd(recv, send *net.UDPConn, aToB bool) {
	buf := make([]byte, 65535)
	for rs.running {
		recv.SetReadDeadline(time.Now().Add(time.Second))
		n, addr, err := recv.ReadFromUDP(buf)
		if err != nil { continue }
		rs.smu.Lock()
		if aToB { rs.callerAddr = addr } else { rs.calleeAddr = addr }
		peer := rs.calleeAddr
		if !aToB { peer = rs.callerAddr }
		rs.smu.Unlock()
		if peer != nil { send.WriteToUDP(buf[:n], peer) }
	}
}

// RtpRelay manages relay sessions with port allocation.
type RtpRelay struct {
	PortMin, PortMax int
	nextPort         int
	sessions         map[string]*RtpRelaySession
	rmu              sync.Mutex
}

func NewRtpRelay(portMin, portMax int) *RtpRelay {
	return &RtpRelay{PortMin: portMin, PortMax: portMax, nextPort: portMin,
		sessions: make(map[string]*RtpRelaySession)}
}

func (r *RtpRelay) allocPorts(n int) []int {
	r.rmu.Lock()
	defer r.rmu.Unlock()
	if r.nextPort%2 != 0 { r.nextPort++ }
	ports := make([]int, n)
	for i := range ports {
		if r.nextPort > r.PortMax { r.nextPort = r.PortMin }
		ports[i] = r.nextPort
		r.nextPort++
	}
	return ports
}

func (r *RtpRelay) CreateSession(id string) (*RtpRelaySession, error) {
	if id == "" { id = fmt.Sprintf("rtp-%d", time.Now().UnixNano()) }
	ports := r.allocPorts(2)
	s := &RtpRelaySession{SessionID: id, LocalPortA: ports[0], LocalPortB: ports[1]}
	if err := s.Start(); err != nil { return nil, err }
	r.rmu.Lock()
	r.sessions[id] = s
	r.rmu.Unlock()
	return s, nil
}

func (r *RtpRelay) DestroySession(id string) {
	r.rmu.Lock()
	s, ok := r.sessions[id]
	delete(r.sessions, id)
	r.rmu.Unlock()
	if ok { s.Stop() }
}

func (r *RtpRelay) ListSessions() []string {
	r.rmu.Lock()
	defer r.rmu.Unlock()
	out := make([]string, 0, len(r.sessions))
	for k := range r.sessions { out = append(out, k) }
	return out
}

func (r *RtpRelay) StopAll() {
	r.rmu.Lock()
	all := make([]*RtpRelaySession, 0)
	for _, s := range r.sessions { all = append(all, s) }
	r.sessions = make(map[string]*RtpRelaySession)
	r.rmu.Unlock()
	for _, s := range all { s.Stop() }
}
