// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package conference — IMS conference management.
//
// Spec anchors (PDFs under specs/3gpp/):
//
//   - TS 24.147 §5.2.3   Conferencing Application Server — the
//                          functional entity this package
//                          implements (the focus role and its
//                          policies).
//   - TS 24.147 §5.3.2   Conference Focus role — handles the
//                          per-conference SIP signalling for
//                          all participants (Create / Join /
//                          Leave / End below).
//   - TS 24.147 §5.3.3   Conference Notification Service — the
//                          BuildConferenceEventXML() helper feeds
//                          this; subscribers get conference-info
//                          NOTIFY documents.
//
// IETF anchors:
//   - RFC 4575           SIP "conference" event package — XML
//                          format used by BuildConferenceEventXML.
//   - RFC 4579           SIP call-control conferencing for UAs —
//                          the call-control verbs (Refer-to with
//                          Cid-URL) that drive participant adds.
//
// TODO(spec: TS 24.147 §5.3.2): the §5.3.2 focus role enumerates
// SIP procedures (REFER for participant invitation, INFO for
// floor mute control, etc.) that this package does not yet emit.
// Today CreateConference returns a URI but the AS does not drive
// the SIP REFER signalling; callers must do so externally.
//
// TODO(spec: TS 24.147 §5.3.3): conference notification SUBSCRIBE
// management — the AS should accept SUBSCRIBE for the conference
// event package, store the dialog, and emit NOTIFY whenever the
// participant set changes. BuildConferenceEventXML produces the
// body but no subscription state is tracked.
package conference

import (
	"fmt"
	"sync"
	"time"

	"github.com/mmt/mmt-studio-core/oam/logger"
)

var log = logger.Get("ims.conference")

// Conference represents an active IMS conference.
type Conference struct {
	ID           string                 `json:"id"`
	HostIMPU     string                 `json:"host_impu"`
	Participants map[string]*Participant `json:"participants"`
	CreatedAt    time.Time              `json:"created_at"`
	State        string                 `json:"state"` // active, ended
}

// Participant is a conference participant.
type Participant struct {
	IMPU      string `json:"impu"`
	JoinedAt  time.Time `json:"joined_at"`
	MediaType string `json:"media_type"` // audio, audio+video
	Muted     bool   `json:"muted"`
}

// ConferenceAS is the Conference Application Server.
type ConferenceAS struct {
	mu          sync.Mutex
	conferences map[string]*Conference
	domain      string
	counter     int

	// MaxParticipants caps the per-conference participant count
	// (host + referred). TS 24.147 v19.0.0 §5.3.2.2 verbatim:
	// "If a request is received by the conference focus that
	// violates the policy of the conference focus, the conference
	// focus shall return an appropriate 4xx response." Hitting this
	// limit makes JoinConference return ErrConferenceFull so the
	// CSCF REFER handler can return 486 Busy Here.
	// Default 6 — matches the operator policy the conference test
	// suite (TC-IMS-014/019/020) is built against.
	MaxParticipants int
}

// ErrConferenceFull is returned by JoinConference when the conference
// is at MaxParticipants. The CSCF maps this to 486 Busy Here per
// TS 24.147 §5.3.2.2 / RFC 3261 §21.4.18.
var ErrConferenceFull = fmt.Errorf("conference at MaxParticipants capacity")

// NewConferenceAS creates a conference AS.
func NewConferenceAS(domain string) *ConferenceAS {
	return &ConferenceAS{
		conferences:     make(map[string]*Conference),
		domain:          domain,
		MaxParticipants: 6,
	}
}

// IsConferenceURI checks if a SIP URI is a conference URI.
func IsConferenceURI(uri string) bool {
	for _, prefix := range []string{"sip:conf-", "sip:conference-factory@"} {
		if len(uri) >= len(prefix) && uri[:len(prefix)] == prefix {
			return true
		}
	}
	return false
}

// CreateConference creates a new conference and returns its URI.
func (cas *ConferenceAS) CreateConference(hostIMPU string) (*Conference, string) {
	cas.mu.Lock()
	defer cas.mu.Unlock()
	cas.counter++
	id := fmt.Sprintf("conf-%d", cas.counter)
	uri := fmt.Sprintf("sip:%s@%s", id, cas.domain)
	c := &Conference{
		ID: id, HostIMPU: hostIMPU, State: "active",
		Participants: make(map[string]*Participant), CreatedAt: time.Now(),
	}
	c.Participants[hostIMPU] = &Participant{IMPU: hostIMPU, JoinedAt: time.Now(), MediaType: "audio"}
	cas.conferences[id] = c
	log.Infof("Conference created: id=%s host=%s uri=%s", id, hostIMPU, uri)
	return c, uri
}

// JoinConference adds a participant.
//
// Returns ErrConferenceFull when the conference is at the
// MaxParticipants cap (TS 24.147 §5.3.2.2 — focus enforces conference
// policy). Already-joined IMPUs are idempotent and don't count toward
// the cap.
func (cas *ConferenceAS) JoinConference(confID, impu, mediaType string) error {
	cas.mu.Lock()
	defer cas.mu.Unlock()
	c, ok := cas.conferences[confID]
	if !ok || c.State != "active" {
		return fmt.Errorf("conference %s not found or ended", confID)
	}
	if _, already := c.Participants[impu]; !already {
		if cas.MaxParticipants > 0 && len(c.Participants) >= cas.MaxParticipants {
			log.Infof("Conference %s: rejecting %s — at MaxParticipants=%d",
				confID, impu, cas.MaxParticipants)
			return ErrConferenceFull
		}
	}
	if mediaType == "" { mediaType = "audio" }
	c.Participants[impu] = &Participant{IMPU: impu, JoinedAt: time.Now(), MediaType: mediaType}
	log.Infof("Conference %s: %s joined (media=%s, %d/%d)",
		confID, impu, mediaType, len(c.Participants), cas.MaxParticipants)
	return nil
}

// LeaveConference removes a participant.
func (cas *ConferenceAS) LeaveConference(confID, impu string) {
	cas.mu.Lock()
	defer cas.mu.Unlock()
	c, ok := cas.conferences[confID]
	if !ok { return }
	delete(c.Participants, impu)
	log.Infof("Conference %s: %s left", confID, impu)
	if len(c.Participants) == 0 { c.State = "ended" }
}

// EndConference terminates a conference.
func (cas *ConferenceAS) EndConference(confID string) {
	cas.mu.Lock()
	defer cas.mu.Unlock()
	if c, ok := cas.conferences[confID]; ok {
		c.State = "ended"
		log.Infof("Conference %s ended", confID)
	}
}

// GetConference returns conference info.
func (cas *ConferenceAS) GetConference(confID string) *Conference {
	cas.mu.Lock()
	defer cas.mu.Unlock()
	return cas.conferences[confID]
}

// ListConferences returns all conferences.
func (cas *ConferenceAS) ListConferences() []*Conference {
	cas.mu.Lock()
	defer cas.mu.Unlock()
	out := make([]*Conference, 0, len(cas.conferences))
	for _, c := range cas.conferences { out = append(out, c) }
	return out
}

// BuildConferenceEventXML generates conference event package XML (RFC 4575).
func BuildConferenceEventXML(c *Conference, confURI string) string {
	xml := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<conference-info entity="%s" state="full" version="1">
  <conference-description>
    <subject>Conference %s</subject>
  </conference-description>
  <users>`, confURI, c.ID)
	for _, p := range c.Participants {
		xml += fmt.Sprintf(`
    <user entity="%s" state="full">
      <endpoint entity="%s">
        <media id="1"><type>%s</type><status>sendrecv</status></media>
      </endpoint>
    </user>`, p.IMPU, p.IMPU, p.MediaType)
	}
	xml += `
  </users>
</conference-info>`
	return xml
}
