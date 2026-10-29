// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package mrfp — Media Resource Function Processor.
//
// The MRFP is the media-plane half of the Multimedia Resource
// Function (MRF) — TS 23.228 §4.7 splits the MRF into MRFC
// (controller) and MRFP (processor); this package implements the
// processor side: audio mixer, video compositor, and per-
// participant RTP send/receive sessions.
//
// Spec anchors (PDFs under specs/3gpp/):
//
//   - TS 23.228 §4.7   Multimedia Resource Function entity split
//                        (MRFC controls; MRFP processes media).
//
// IETF anchors:
//   - RFC 3550 §5      RTP fixed header layout (V=2, P, X, CC, M,
//                        PT, sequence number, timestamp, SSRC) —
//                        BuildRTPPacket / ParseRTPHeader implement
//                        this layout.
//   - RFC 3551         RTP profile for audio/video — payload type
//                        assignments. The fixed PayloadTypeL16=96
//                        constant is the dynamic-PT range; static
//                        PT mapping per §6 of RFC 3551 is not
//                        encoded here.
//
// TODO(spec: RFC 3550 §6 / RFC 3611): RTCP SR/RR generation +
// RTCP-XR media-quality reports are not built; the per-session
// stats stay in-process and are not surfaced on the wire.
//
// TODO(spec: TS 23.228 §4.7): MRFP-level transcoding (e.g. AMR
// ↔ G.711 ↔ Opus) is not implemented. ParticipantRtpSession
// forwards payload bytes verbatim; mixed-codec conferences need
// transcoding hooks here.
package mrfp

import (
	"encoding/binary"
	"math"
	"net"
	"sync"
	"time"

	"github.com/mmt/mmt-studio-core/oam/logger"
)

var log = logger.Get("ims.mrfp")

const (
	SampleRate     = 16000
	FrameMS        = 20
	FrameSamples   = SampleRate * FrameMS / 1000
	FrameBytes     = FrameSamples * 2
	PayloadTypeL16 = 96
	RTPHeaderSize  = 12
)

func BuildRTPPacket(pt byte, seq uint16, ts, ssrc uint32, payload []byte, marker bool) []byte {
	hdr := make([]byte, 12)
	hdr[0] = 0x80
	mpt := pt & 0x7F
	if marker { mpt |= 0x80 }
	hdr[1] = mpt
	binary.BigEndian.PutUint16(hdr[2:4], seq)
	binary.BigEndian.PutUint32(hdr[4:8], ts)
	binary.BigEndian.PutUint32(hdr[8:12], ssrc)
	return append(hdr, payload...)
}

func ParseRTPHeader(data []byte) (pt byte, seq uint16, ts, ssrc uint32, payload []byte, ok bool) {
	if len(data) < RTPHeaderSize { return 0, 0, 0, 0, nil, false }
	if (data[0]>>6)&0x3 != 2 { return 0, 0, 0, 0, nil, false }
	cc := data[0] & 0x0F
	ext := (data[0] >> 4) & 0x1
	pt = data[1] & 0x7F
	seq = binary.BigEndian.Uint16(data[2:4])
	ts = binary.BigEndian.Uint32(data[4:8])
	ssrc = binary.BigEndian.Uint32(data[8:12])
	off := RTPHeaderSize + int(cc)*4
	if ext != 0 && len(data) > off+4 {
		el := binary.BigEndian.Uint16(data[off+2 : off+4])
		off += 4 + int(el)*4
	}
	if off > len(data) { off = len(data) }
	return pt, seq, ts, ssrc, data[off:], true
}

// ParticipantRtpSession manages RTP recv/send for one conference participant.
type ParticipantRtpSession struct {
	ID, Host  string
	LocalPort int
	SSRC      uint32
	sock      *net.UDPConn
	running   bool
	peerAddr  *net.UDPAddr
	mu        sync.Mutex
	latest    []byte
	latestPT  byte
	sendSeq   uint16
	sendTS    uint32
}

func NewParticipantRtp(id string, port int, host string) *ParticipantRtpSession {
	h := uint32(0)
	for _, c := range id { h = h*31 + uint32(c) }
	return &ParticipantRtpSession{ID: id, LocalPort: port, Host: host, SSRC: h}
}

func (p *ParticipantRtpSession) Start() error {
	var err error
	p.sock, err = net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP(p.Host), Port: p.LocalPort})
	if err != nil { return err }
	p.running = true
	go p.recv()
	return nil
}
func (p *ParticipantRtpSession) Stop() {
	p.running = false
	if p.sock != nil { p.sock.Close() }
}
func (p *ParticipantRtpSession) recv() {
	buf := make([]byte, 4096)
	for p.running {
		p.sock.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		n, addr, err := p.sock.ReadFromUDP(buf)
		if err != nil { continue }
		p.mu.Lock()
		if p.peerAddr == nil { p.peerAddr = addr }
		pt, _, _, _, payload, ok := ParseRTPHeader(buf[:n])
		if ok && len(payload) > 0 {
			p.latest = make([]byte, len(payload))
			copy(p.latest, payload)
			p.latestPT = pt
		}
		p.mu.Unlock()
	}
}
func (p *ParticipantRtpSession) GetLatest() ([]byte, byte) {
	p.mu.Lock(); defer p.mu.Unlock()
	return p.latest, p.latestPT
}
func (p *ParticipantRtpSession) SendRTP(payload []byte, pt byte) {
	p.mu.Lock(); peer := p.peerAddr; p.mu.Unlock()
	if peer == nil || p.sock == nil { return }
	pkt := BuildRTPPacket(pt, p.sendSeq, p.sendTS, p.SSRC, payload, false)
	p.sock.WriteToUDP(pkt, peer)
	p.sendSeq++; p.sendTS += uint32(FrameSamples)
}

// AudioMixer mixes PCM from N participants.
type AudioMixer struct {
	ConfID string
	parts  map[string]*ParticipantRtpSession
	levels map[string]float64
	Active string
	OnSpkr func(string)
	run    bool
	mu     sync.Mutex
}

func NewAudioMixer(id string) *AudioMixer {
	return &AudioMixer{ConfID: id, parts: map[string]*ParticipantRtpSession{}, levels: map[string]float64{}}
}
func (m *AudioMixer) Add(pid string, r *ParticipantRtpSession) { m.mu.Lock(); m.parts[pid] = r; m.levels[pid] = 0; m.mu.Unlock() }
func (m *AudioMixer) Remove(pid string) { m.mu.Lock(); delete(m.parts, pid); delete(m.levels, pid); m.mu.Unlock() }
func (m *AudioMixer) Start() { m.run = true; go m.loop(); log.Infof("MRFP mixer started conf=%s", m.ConfID) }
func (m *AudioMixer) Stop()  { m.run = false }

func (m *AudioMixer) loop() {
	for m.run {
		t := time.Now()
		m.mu.Lock()
		if len(m.parts) < 2 { m.mu.Unlock(); time.Sleep(20 * time.Millisecond); continue }
		frames := map[string][]int16{}
		for pid, r := range m.parts {
			pl, pt := r.GetLatest()
			frames[pid] = decode(pl, pt)
			m.levels[pid] = energy(frames[pid])
		}
		top, topE := "", float64(0)
		for pid, e := range m.levels { if e > topE { topE = e; top = pid } }
		if topE > 100 && top != m.Active { m.Active = top; if m.OnSpkr != nil { go m.OnSpkr(top) } }
		for pid, r := range m.parts { r.SendRTP(encode(mixEx(frames, pid)), PayloadTypeL16) }
		m.mu.Unlock()
		if d := 20*time.Millisecond - time.Since(t); d > 0 { time.Sleep(d) }
	}
}

func decode(pl []byte, pt byte) []int16 {
	if pt == PayloadTypeL16 && len(pl) >= FrameBytes {
		s := make([]int16, FrameSamples)
		for i := range s { s[i] = int16(binary.BigEndian.Uint16(pl[i*2:])) }
		return s
	}
	if len(pl) >= 4 {
		s := make([]int16, FrameSamples)
		r := float64(len(pl)) / float64(FrameSamples)
		for i := range s { idx := int(float64(i) * r); if idx < len(pl) { s[i] = int16((int(pl[idx]) - 128) * 256) } }
		return s
	}
	return make([]int16, FrameSamples)
}
func encode(s []int16) []byte { o := make([]byte, len(s)*2); for i, v := range s { binary.BigEndian.PutUint16(o[i*2:], uint16(v)) }; return o }
func energy(s []int16) float64 { if len(s)==0{return 0}; sq := float64(0); for _,v := range s { sq += float64(v)*float64(v) }; return math.Sqrt(sq/float64(len(s))) }
func mixEx(f map[string][]int16, ex string) []int16 {
	r := make([]int16, FrameSamples)
	for pid, pcm := range f {
		if pid == ex { continue }
		for i := 0; i < FrameSamples && i < len(pcm); i++ {
			v := int(r[i]) + int(pcm[i])
			if v > 32767 { v = 32767 }
			if v < -32768 { v = -32768 }
			r[i] = int16(v)
		}
	}
	return r
}

// VideoCompositor forwards active speaker's video.
type VideoCompositor struct {
	ConfID string
	parts  map[string]*ParticipantRtpSession
	active string
	run    bool
	mu     sync.Mutex
}
func NewVideoCompositor(id string) *VideoCompositor { return &VideoCompositor{ConfID: id, parts: map[string]*ParticipantRtpSession{}} }
func (v *VideoCompositor) Add(pid string, r *ParticipantRtpSession) { v.mu.Lock(); v.parts[pid] = r; v.mu.Unlock() }
func (v *VideoCompositor) Remove(pid string) { v.mu.Lock(); delete(v.parts, pid); v.mu.Unlock() }
func (v *VideoCompositor) SetActive(pid string) { v.mu.Lock(); v.active = pid; v.mu.Unlock() }
func (v *VideoCompositor) Start() { v.run = true; go v.fwd() }
func (v *VideoCompositor) Stop()  { v.run = false }
func (v *VideoCompositor) fwd() {
	for v.run {
		v.mu.Lock()
		if len(v.parts) < 2 || v.active == "" { v.mu.Unlock(); time.Sleep(5*time.Millisecond); continue }
		sp := v.parts[v.active]
		if sp == nil { v.mu.Unlock(); time.Sleep(5*time.Millisecond); continue }
		pl, pt := sp.GetLatest()
		if len(pl) > 0 { for pid, s := range v.parts { if pid != v.active { s.SendRTP(pl, pt) } } }
		v.mu.Unlock(); time.Sleep(time.Millisecond)
	}
}

// MixerSession per conference.
type MixerSession struct {
	ConfID string
	Audio  *AudioMixer
	Video  *VideoCompositor
	Parts  map[string]map[string]*ParticipantRtpSession
}

// MRFP controller.
type MRFP struct {
	Host, AdvIP    string
	PortMin, PortMax int
	next           int
	sess           map[string]*MixerSession
	mu             sync.Mutex
}

func New(host string, pmin, pmax int) *MRFP {
	return &MRFP{Host: host, PortMin: pmin, PortMax: pmax, next: pmin, sess: map[string]*MixerSession{}, AdvIP: "127.0.0.1"}
}
func (m *MRFP) alloc() int {
	m.mu.Lock(); defer m.mu.Unlock()
	p := m.next; if p%2!=0{p++}; if p>=m.PortMax{p=m.PortMin; if p%2!=0{p++}}; m.next=p+2; return p
}
func (m *MRFP) Create(cid string) *MixerSession {
	m.mu.Lock(); if s,ok:=m.sess[cid];ok{m.mu.Unlock();return s}
	ms := &MixerSession{ConfID:cid, Audio:NewAudioMixer(cid), Video:NewVideoCompositor(cid), Parts:map[string]map[string]*ParticipantRtpSession{}}
	ms.Audio.OnSpkr = ms.Video.SetActive; ms.Audio.Start(); ms.Video.Start(); m.sess[cid]=ms; m.mu.Unlock(); return ms
}
func (m *MRFP) Destroy(cid string) {
	m.mu.Lock(); ms,ok:=m.sess[cid]; delete(m.sess,cid); m.mu.Unlock()
	if ok { ms.Audio.Stop(); ms.Video.Stop(); for _,ps:=range ms.Parts { for _,s:=range ps { if s!=nil{s.Stop()} } } }
}
func (m *MRFP) AddParticipant(cid, pid string, media []string) map[string]interface{} {
	ms := m.Create(cid); r := map[string]interface{}{"ip":m.AdvIP,"audio_port":nil,"video_port":nil}
	ap := m.alloc(); ar := NewParticipantRtp(pid+"_audio", ap, m.Host); ar.Start(); ms.Audio.Add(pid, ar); r["audio_port"]=ap
	ps := map[string]*ParticipantRtpSession{"audio":ar}
	for _,mt:=range media { if mt=="video" { vp:=m.alloc(); vr:=NewParticipantRtp(pid+"_video",vp,m.Host); vr.Start(); ms.Video.Add(pid,vr); r["video_port"]=vp; ps["video"]=vr } }
	ms.Parts[pid]=ps; return r
}
func (m *MRFP) RemoveParticipant(cid, pid string) {
	m.mu.Lock(); ms:=m.sess[cid]; m.mu.Unlock(); if ms==nil{return}
	ps:=ms.Parts[pid]; delete(ms.Parts,pid); for _,s:=range ps { if s!=nil{s.Stop()} }
	ms.Audio.Remove(pid); ms.Video.Remove(pid); if len(ms.Parts)==0{m.Destroy(cid)}
}
func (m *MRFP) ListSessions() map[string]interface{} {
	m.mu.Lock(); defer m.mu.Unlock(); out:=map[string]interface{}{}
	for cid,ms:=range m.sess { pids:=[]string{}; for p:=range ms.Parts{pids=append(pids,p)}; out[cid]=map[string]interface{}{"participants":pids,"active_speaker":ms.Audio.Active} }
	return out
}
