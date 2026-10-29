// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package smsf -- SMS Function (TS 23.502 §4.13.3 -- SMS over NAS).
//
// Go port of nf/smsf/. Handles MO/MT SMS:
//   - SMS codec (TS 23.040 §9.2 TPDU + TS 24.011 §7.2 CP / §7.3 RP)
//   - Session context management (singleton, thread-safe)
//   - SMS delivery, routing, store-and-forward
//   - Message expiry (TS 23.040 §9.2.3.12 TP-VP)
package smsf

import (
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	"github.com/mmt/mmt-studio-core/db/engine"
	"github.com/mmt/mmt-studio-core/oam/logger"
)

var log = logger.Get("smsf")

// ================================================================
// GSM 7-bit Default Alphabet (TS 23.038 §6.2.1)
// ================================================================

var gsm7Basic = []rune(
	"@\u00a3$\u00a5\u00e8\u00e9\u00f9\u00ec\u00f2\u00c7\n\u00d8\u00f8\r\u00c5\u00e5" +
		"\u0394_\u03a6\u0393\u039b\u03a9\u03a0\u03a8\u03a3\u0398\u039e\x1b\u00c6\u00e6\u00df\u00c9" +
		" !\"#\u00a4%&'()*+,-./0123456789:;<=>?" +
		"\u00a1ABCDEFGHIJKLMNOPQRSTUVWXYZ\u00c4\u00d6\u00d1\u00dc\u00a7" +
		"\u00bfabcdefghijklmnopqrstuvwxyz\u00e4\u00f6\u00f1\u00fc\u00e0")

var gsm7Ext = map[byte]rune{
	0x0A: '\x0c', 0x14: '^', 0x28: '{', 0x29: '}', 0x2F: '\\',
	0x3C: '[', 0x3D: '~', 0x3E: ']', 0x40: '|', 0x65: '\u20ac',
}

type gsm7Entry struct {
	val   byte
	isExt bool
}

var charToGSM7 map[rune]gsm7Entry

func init() {
	charToGSM7 = make(map[rune]gsm7Entry)
	for i, c := range gsm7Basic {
		if c != 0x1b {
			charToGSM7[c] = gsm7Entry{byte(i), false}
		}
	}
	for code, c := range gsm7Ext {
		charToGSM7[c] = gsm7Entry{code, true}
	}
}

// IsGSM7Encodable checks if all characters can be encoded in GSM 7-bit.
func IsGSM7Encodable(text string) bool {
	for _, ch := range text {
		if _, ok := charToGSM7[ch]; !ok {
			return false
		}
	}
	return true
}

// GSM7Encode encodes a Unicode string to GSM 7-bit packed bytes.
// Returns packed bytes and number of septets.
func GSM7Encode(text string) ([]byte, int) {
	var septets []byte
	for _, ch := range text {
		entry, ok := charToGSM7[ch]
		if ok {
			if entry.isExt {
				septets = append(septets, 0x1B)
			}
			septets = append(septets, entry.val)
		} else {
			septets = append(septets, 0x3F) // '?'
		}
	}
	numSeptets := len(septets)

	// Pack septets into octets (TS 23.038 §6.1.2)
	var result []byte
	shift := 0
	for i := 0; i < len(septets); i++ {
		if shift == 7 {
			shift = 0
			continue
		}
		octet := septets[i] >> uint(shift)
		if i+1 < len(septets) {
			octet |= septets[i+1] << uint(7-shift)
		}
		result = append(result, octet)
		shift++
	}
	return result, numSeptets
}

// GSM7Decode decodes GSM 7-bit packed bytes to a Unicode string.
func GSM7Decode(data []byte, numSeptets int) string {
	// Unpack septets
	septets := unpackSeptets(data, numSeptets)
	var chars []rune
	i := 0
	for i < len(septets) {
		if septets[i] == 0x1B && i+1 < len(septets) {
			if c, ok := gsm7Ext[septets[i+1]]; ok {
				chars = append(chars, c)
			} else {
				chars = append(chars, '?')
			}
			i += 2
		} else {
			if int(septets[i]) < len(gsm7Basic) {
				chars = append(chars, gsm7Basic[septets[i]])
			} else {
				chars = append(chars, '?')
			}
			i++
		}
	}
	return string(chars)
}

func unpackSeptets(data []byte, numSeptets int) []byte {
	var septets []byte
	bitOffset := 0
	for len(septets) < numSeptets {
		byteIdx := bitOffset / 8
		bitIdx := bitOffset % 8
		if byteIdx >= len(data) {
			break
		}
		var val uint16
		val = uint16(data[byteIdx])
		if byteIdx+1 < len(data) {
			val |= uint16(data[byteIdx+1]) << 8
		}
		septet := byte((val >> uint(bitIdx)) & 0x7F)
		septets = append(septets, septet)
		bitOffset += 7
	}
	return septets
}

// ================================================================
// Address Encoding (TS 23.040 §9.1.2.5)
// ================================================================

// EncodeAddress encodes an MSISDN per TS 23.040 §9.1.2.5.
func EncodeAddress(msisdn string) []byte {
	digits := strings.TrimLeft(msisdn, "+")
	numDigits := len(digits)
	toa := byte(0x81)
	if strings.HasPrefix(msisdn, "+") {
		toa = 0x91
	}
	var bcd []byte
	for i := 0; i < len(digits); i += 2 {
		d1 := digits[i] - '0'
		d2 := byte(0x0F)
		if i+1 < len(digits) {
			d2 = digits[i+1] - '0'
		}
		bcd = append(bcd, (d2<<4)|d1)
	}
	result := []byte{byte(numDigits), toa}
	return append(result, bcd...)
}

// DecodeAddress decodes a TP-DA/TP-OA address from raw bytes.
func DecodeAddress(data []byte, offset int) (string, int) {
	if offset >= len(data) {
		return "", 0
	}
	addrLen := int(data[offset])
	toa := data[offset+1]
	numBCD := (addrLen + 1) / 2
	bcdData := data[offset+2 : offset+2+numBCD]

	var digits []byte
	for _, b := range bcdData {
		lo := b & 0x0F
		hi := (b >> 4) & 0x0F
		if lo <= 9 {
			digits = append(digits, '0'+lo)
		}
		if hi <= 9 {
			digits = append(digits, '0'+hi)
		}
	}
	msisdn := string(digits[:addrLen])
	if (toa & 0x70) == 0x10 {
		msisdn = "+" + msisdn
	}
	return msisdn, 2 + numBCD
}

// ================================================================
// SMS-SUBMIT / SMS-DELIVER TPDU (TS 23.040 §9.2.2.1 / §9.2.2.2)
// ================================================================

// EncodeSMSSubmit encodes an SMS-SUBMIT TPDU.
func EncodeSMSSubmit(mr byte, daMSISDN, text, encoding string, udh []byte) []byte {
	firstOctet := byte(0x01)
	if len(udh) > 0 {
		firstOctet |= 0x40
	}
	tpDA := EncodeAddress(daMSISDN)
	tpPID := byte(0x00)
	tpDCS := byte(0x00)
	if encoding == "ucs2" {
		tpDCS = 0x08
	}

	var udPayload []byte
	var tpUDL int
	if encoding == "ucs2" {
		udBytes := encodeUTF16BE(text)
		if len(udh) > 0 {
			udPayload = append(udh, udBytes...)
		} else {
			udPayload = udBytes
		}
		tpUDL = len(udPayload)
	} else {
		packed, numSeptets := GSM7Encode(text)
		if len(udh) > 0 {
			udhLen := len(udh)
			fillBits := (7 - ((udhLen * 8) % 7)) % 7
			tpUDL = numSeptets + ((udhLen*8 + fillBits) / 7)
			pad := make([]byte, (fillBits+7)/8)
			udPayload = append(udh, pad...)
			udPayload = append(udPayload, packed...)
		} else {
			udPayload = packed
			tpUDL = numSeptets
		}
	}

	var result []byte
	result = append(result, firstOctet, mr)
	result = append(result, tpDA...)
	result = append(result, tpPID, tpDCS, byte(tpUDL))
	result = append(result, udPayload...)
	return result
}

// EncodeSMSDeliver encodes an SMS-DELIVER TPDU.
func EncodeSMSDeliver(oaMSISDN, text, encoding string, udh []byte) []byte {
	firstOctet := byte(0x04) // MTI=00, MMS=1
	if len(udh) > 0 {
		firstOctet |= 0x40
	}
	tpOA := EncodeAddress(oaMSISDN)
	tpPID := byte(0x00)
	tpDCS := byte(0x00)
	if encoding == "ucs2" {
		tpDCS = 0x08
	}
	tpSCTS := encodeSCTS()

	var udPayload []byte
	var tpUDL int
	if encoding == "ucs2" {
		udBytes := encodeUTF16BE(text)
		if len(udh) > 0 {
			udPayload = append(udh, udBytes...)
		} else {
			udPayload = udBytes
		}
		tpUDL = len(udPayload)
	} else {
		packed, numSeptets := GSM7Encode(text)
		if len(udh) > 0 {
			udhLen := len(udh)
			fillBits := (7 - ((udhLen * 8) % 7)) % 7
			tpUDL = numSeptets + ((udhLen*8 + fillBits) / 7)
			pad := make([]byte, (fillBits+7)/8)
			udPayload = append(udh, pad...)
			udPayload = append(udPayload, packed...)
		} else {
			udPayload = packed
			tpUDL = numSeptets
		}
	}

	var result []byte
	result = append(result, firstOctet)
	result = append(result, tpOA...)
	result = append(result, tpPID, tpDCS)
	result = append(result, tpSCTS...)
	result = append(result, byte(tpUDL))
	result = append(result, udPayload...)
	return result
}

func encodeSCTS() []byte {
	t := time.Now()
	bcdSwap := func(val int) byte {
		hi := val / 10
		lo := val % 10
		return byte((lo << 4) | hi)
	}
	return []byte{
		bcdSwap(t.Year() % 100), bcdSwap(int(t.Month())), bcdSwap(t.Day()),
		bcdSwap(t.Hour()), bcdSwap(t.Minute()), bcdSwap(t.Second()),
		0x00, // UTC
	}
}

func encodeUTF16BE(text string) []byte {
	runes := []rune(text)
	u16 := utf16.Encode(runes)
	result := make([]byte, len(u16)*2)
	for i, v := range u16 {
		result[i*2] = byte(v >> 8)
		result[i*2+1] = byte(v)
	}
	return result
}

// ================================================================
// UDH for Concatenated SMS (TS 23.040 §9.2.3.24.1)
// ================================================================

// BuildConcatUDH builds a UDH for concatenated (multipart) SMS.
func BuildConcatUDH(refNum, totalParts, partNum int, use16bit bool) []byte {
	if use16bit {
		ied := []byte{byte(refNum >> 8), byte(refNum), byte(totalParts), byte(partNum)}
		udhl := byte(1 + 1 + len(ied))
		result := []byte{udhl, 0x08, byte(len(ied))}
		return append(result, ied...)
	}
	ied := []byte{byte(refNum & 0xFF), byte(totalParts), byte(partNum)}
	udhl := byte(1 + 1 + len(ied))
	result := []byte{udhl, 0x00, byte(len(ied))}
	return append(result, ied...)
}

// ================================================================
// CP-DATA / RP-DATA framing (TS 24.011 §7.2 CP / §7.3 RP)
// ================================================================

const (
	CPData  = 0x01
	CPAck   = 0x04
	CPError = 0x10

	RPDataMSToNet  = 0x00
	RPDataNetToMS  = 0x01
	RPAckMSToNet   = 0x02
	RPAckNetToMS   = 0x03
	RPErrorMSToNet = 0x04
	RPErrorNetToMS = 0x05
)

// EncodeCPData encodes a CP-DATA PDU wrapping an RP-PDU.
func EncodeCPData(ti byte, rpPDU []byte) []byte {
	pdTI := (ti << 4) | 0x09
	return append([]byte{pdTI, CPData, byte(len(rpPDU))}, rpPDU...)
}

// EncodeCPAck encodes a CP-ACK PDU.
func EncodeCPAck(ti byte) []byte {
	return []byte{(ti << 4) | 0x09, CPAck}
}

// EncodeCPError encodes a CP-ERROR PDU.
func EncodeCPError(ti, cause byte) []byte {
	return []byte{(ti << 4) | 0x09, CPError, cause}
}

// EncodeRPDataMT encodes an RP-DATA Net→MS wrapping a TPDU per
// TS 24.011 §7.3.1.1.
//
// Layout (TS 24.011 §8.2):
//
//	octet 1   : MTI = 0x00 (RP-DATA Net→MS) — §8.2.2 Table 8.4
//	octet 2   : RP-Message-Reference — §8.2.3
//	octet 3   : Length L1 of RP-OA contents — §8.2.5.1
//	octets..  : RP-OA contents (TOA + BCD digits) when oaSMSC != ""
//	octet ... : Length L2 of RP-DA contents (always 0 here) — §8.2.5.2
//	octet ... : Length of TPDU — §8.2.5.3
//	octets... : TPDU bytes (TS 23.040 §9.2)
//
// Note: the RP-OA element does NOT carry the leading "number of
// digits" octet that the TP-DA / TP-OA fields carry per TS 23.040
// §9.1.2.5 — the BCD-only form here matches TS 24.011 §8.2.5.1
// Figure 8.5 ("Length of RP-Originator Address contents | TON/NPI |
// digit pairs ...").
func EncodeRPDataMT(mr byte, oaSMSC string, tpdu []byte) []byte {
	var result []byte
	result = append(result, RPDataNetToMS, mr)
	if oaSMSC != "" {
		oa := encodeRPAddress(oaSMSC) // TS 24.011 §8.2.5.1 contents only.
		result = append(result, byte(len(oa)))
		result = append(result, oa...)
	} else {
		result = append(result, 0x00) // empty RP-OA per §8.2.5.1.
	}
	result = append(result, 0x00) // empty RP-DA per §8.2.5.2 (Net→MS).
	result = append(result, byte(len(tpdu)))
	return append(result, tpdu...)
}

// EncodeRPAck encodes an RP-ACK per TS 24.011 §7.3.3.
//
// Layout: MTI(1) | RP-Message-Reference(1). The §7.3.3 spec allows
// an optional RP-User-Data IE carrying a Status-Report TPDU; we
// don't emit one (see TS 23.040 §9.2.3.5 TODO in codec_decode.go).
func EncodeRPAck(mr byte, netToMS bool) []byte {
	msgType := byte(RPAckMSToNet)
	if netToMS {
		msgType = RPAckNetToMS
	}
	return []byte{msgType, mr}
}

// EncodeRPError encodes an RP-ERROR per TS 24.011 §7.3.4.
//
// Layout: MTI(1) | RP-Message-Reference(1) | RP-Cause LV (length=1 |
// cause octet). Cause values are per TS 24.011 §8.2.5.4; the high
// bit of the cause octet ("ext") is always 0 in our build because
// we never attach the optional diagnostic field.
func EncodeRPError(mr, cause byte, netToMS bool) []byte {
	msgType := byte(RPErrorMSToNet)
	if netToMS {
		msgType = RPErrorNetToMS
	}
	return []byte{msgType, mr, 0x01, cause & 0x7F}
}

// ================================================================
// Segmentation
// ================================================================

const (
	GSM7SingleLimit  = 160
	GSM7ConcatLimit  = 153
	UCS2SingleLimit  = 70
	UCS2ConcatLimit  = 67
)

// SegmentText splits a long message into segments for concatenated SMS.
func SegmentText(text, encoding string) []string {
	single, perSeg := GSM7SingleLimit, GSM7ConcatLimit
	if encoding == "ucs2" {
		single, perSeg = UCS2SingleLimit, UCS2ConcatLimit
	}
	runes := []rune(text)
	if len(runes) <= single {
		return []string{text}
	}
	var segments []string
	for pos := 0; pos < len(runes); pos += perSeg {
		end := pos + perSeg
		if end > len(runes) {
			end = len(runes)
		}
		segments = append(segments, string(runes[pos:end]))
	}
	return segments
}

// ================================================================
// SMSF Context (singleton)
// ================================================================

// SmsfContext holds active SMS sessions and routing state in-memory.
type SmsfContext struct {
	mu             sync.Mutex
	activeSessions map[string]*smsSession // IMSI -> session
	msgReference   int
}

type smsSession struct {
	TI        int
	State     string // idle, mo_active, mt_active
	PendingMT []int64
}

var (
	ctxOnce sync.Once
	ctx     *SmsfContext
)

// GetContext returns the global SmsfContext singleton.
func GetContext() *SmsfContext {
	ctxOnce.Do(func() {
		ctx = &SmsfContext{
			activeSessions: make(map[string]*smsSession),
		}
		log.Infof("SmsfContext: initialized")
	})
	return ctx
}

func (c *SmsfContext) getSession(imsi string) *smsSession {
	c.mu.Lock()
	defer c.mu.Unlock()
	if s, ok := c.activeSessions[imsi]; ok {
		return s
	}
	s := &smsSession{State: "idle"}
	c.activeSessions[imsi] = s
	return s
}

func (c *SmsfContext) setState(imsi, state string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if s, ok := c.activeSessions[imsi]; ok {
		s.State = state
	}
}

func (c *SmsfContext) queueMT(imsi string, msgID int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.activeSessions[imsi]; !ok {
		c.activeSessions[imsi] = &smsSession{State: "idle"}
	}
	c.activeSessions[imsi].PendingMT = append(c.activeSessions[imsi].PendingMT, msgID)
}

func (c *SmsfContext) dequeueMT(imsi string) (int64, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s, ok := c.activeSessions[imsi]
	if !ok || len(s.PendingMT) == 0 {
		return 0, false
	}
	msgID := s.PendingMT[0]
	s.PendingMT = s.PendingMT[1:]
	return msgID, true
}

// NextReference allocates the next TP-MR value (0-255, wraps).
func (c *SmsfContext) NextReference() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	ref := c.msgReference
	c.msgReference = (c.msgReference + 1) & 0xFF
	return ref
}

// GetAllSessions returns a snapshot of all active SMS sessions.
func (c *SmsfContext) GetAllSessions() map[string]map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]map[string]any, len(c.activeSessions))
	for imsi, s := range c.activeSessions {
		out[imsi] = map[string]any{
			"ti":         s.TI,
			"state":      s.State,
			"pending_mt": len(s.PendingMT),
		}
	}
	return out
}

// GetSessionCount returns the number of active SMS sessions.
func (c *SmsfContext) GetSessionCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.activeSessions)
}

// ================================================================
// UE / MSISDN resolution helpers
// ================================================================

type ueRecord struct {
	ID      int64
	IMSI    string
	MSISDN  string
	Enabled bool
}

func lookupUEByMSISDN(msisdn string) *ueRecord {
	db, err := engine.Open()
	if err != nil {
		return nil
	}
	clean := strings.TrimLeft(msisdn, "+")
	row := db.QueryRow(
		"SELECT id, imsi, msisdn, enabled FROM ue WHERE msisdn=? OR msisdn=? OR msisdn=?",
		msisdn, clean, "+"+clean)
	var ue ueRecord
	var enabled int64
	if err := row.Scan(&ue.ID, &ue.IMSI, &ue.MSISDN, &enabled); err != nil {
		return nil
	}
	ue.Enabled = enabled != 0
	return &ue
}

func lookupUEByIMSI(imsi string) *ueRecord {
	db, err := engine.Open()
	if err != nil {
		return nil
	}
	row := db.QueryRow("SELECT id, imsi, msisdn, enabled FROM ue WHERE imsi=?", imsi)
	var ue ueRecord
	var enabled int64
	if err := row.Scan(&ue.ID, &ue.IMSI, &ue.MSISDN, &enabled); err != nil {
		return nil
	}
	ue.Enabled = enabled != 0
	return &ue
}

func resolveRoute(recipientMSISDN string) map[string]string {
	db, err := engine.Open()
	if err != nil {
		return map[string]string{"route_type": "local"}
	}
	rows, err := db.Query("SELECT msisdn_pattern, route_type, destination FROM sms_routing ORDER BY priority DESC")
	if err != nil {
		return map[string]string{"route_type": "local"}
	}
	defer rows.Close()

	clean := strings.TrimLeft(recipientMSISDN, "+")
	for rows.Next() {
		var pattern, routeType string
		var dest sql.NullString
		if err := rows.Scan(&pattern, &routeType, &dest); err != nil {
			continue
		}
		// Simple LIKE-style matching
		trimmedPattern := strings.Trim(pattern, "%")
		matched := false
		if strings.HasSuffix(pattern, "%") && !strings.HasPrefix(pattern, "%") {
			matched = strings.HasPrefix(clean, trimmedPattern)
		} else if strings.HasPrefix(pattern, "%") && !strings.HasSuffix(pattern, "%") {
			matched = strings.HasSuffix(clean, trimmedPattern)
		} else {
			matched = pattern == clean || pattern == recipientMSISDN
		}
		if matched {
			d := ""
			if dest.Valid {
				d = dest.String
			}
			return map[string]string{"route_type": routeType, "destination": d}
		}
	}
	return map[string]string{"route_type": "local"}
}

// ================================================================
// SMS MO Processing (TS 23.502 §4.13.3.3 MO SMS over NAS in CM-IDLE / §4.13.3.5 in CM-CONNECTED)
// ================================================================

// MOResult is the result of processing a Mobile-Originated SMS.
type MOResult struct {
	OK        bool    `json:"ok"`
	MsgIDs    []int64 `json:"msg_ids,omitempty"`
	Status    string  `json:"status"`
	Segments  int     `json:"segments"`
	Encoding  string  `json:"encoding"`
	Reference *int    `json:"reference,omitempty"`
	Error     string  `json:"error,omitempty"`
}

// ProcessMOSMS processes a Mobile-Originated SMS.
func ProcessMOSMS(senderIMSI, recipientMSISDN, text, encoding string) MOResult {
	c := GetContext()

	// 1. Validate sender
	sender := lookupUEByIMSI(senderIMSI)
	if sender == nil {
		return MOResult{OK: false, Error: "Unknown sender IMSI"}
	}
	if !sender.Enabled {
		return MOResult{OK: false, Error: "Sender UE disabled"}
	}
	senderMSISDN := sender.MSISDN
	if senderMSISDN == "" {
		senderMSISDN = senderIMSI
	}

	// 2. Auto-detect encoding
	if encoding == "gsm7" && !IsGSM7Encodable(text) {
		encoding = "ucs2"
	}

	// 3. Segment
	segments := SegmentText(text, encoding)
	numSegments := len(segments)
	var concatRef *int
	if numSegments > 1 {
		ref := c.NextReference()
		concatRef = &ref
	}

	log.WithIMSI(senderIMSI).Infof("MO-SMS: -> %s, %d segment(s), encoding=%s",
		recipientMSISDN, numSegments, encoding)

	// 4. Store in DB
	db, err := engine.Open()
	if err != nil {
		return MOResult{OK: false, Error: "DB error: " + err.Error()}
	}

	var msgIDs []int64
	for _, segText := range segments {
		ref := 0
		if concatRef != nil {
			ref = *concatRef
		} else {
			ref = c.NextReference()
		}
		result, err := db.Exec(
			`INSERT INTO sms_messages
			 (sender_imsi, sender_msisdn, recipient_msisdn, direction,
			  tp_da, tp_oa, tp_ud, encoding, status, segments, reference)
			 VALUES (?, ?, ?, 'MO', ?, ?, ?, ?, 'pending', ?, ?)`,
			senderIMSI, senderMSISDN, recipientMSISDN,
			recipientMSISDN, senderMSISDN, segText, encoding, numSegments, ref)
		if err != nil {
			continue
		}
		id, _ := result.LastInsertId()
		msgIDs = append(msgIDs, id)
	}

	// 5. Update session state
	c.setState(senderIMSI, "mo_active")

	// 6. Route and deliver
	route := resolveRoute(recipientMSISDN)
	deliveryStatus := "pending"

	if route["route_type"] == "local" {
		deliveryStatus = deliverLocal(msgIDs, recipientMSISDN, senderMSISDN,
			segments, encoding, concatRef, numSegments)
	}

	// 7. Update status in DB
	now := float64(time.Now().Unix())
	for _, mid := range msgIDs {
		switch deliveryStatus {
		case "delivered":
			_, _ = db.Exec("UPDATE sms_messages SET status='delivered', delivered_at=? WHERE id=?", now, mid)
		case "failed":
			_, _ = db.Exec("UPDATE sms_messages SET status='failed' WHERE id=?", mid)
		}
	}

	c.setState(senderIMSI, "idle")

	return MOResult{
		OK:        true,
		MsgIDs:    msgIDs,
		Status:    deliveryStatus,
		Segments:  numSegments,
		Encoding:  encoding,
		Reference: concatRef,
	}
}

func deliverLocal(msgIDs []int64, recipientMSISDN, senderMSISDN string,
	segments []string, encoding string, concatRef *int, numSegments int) string {
	_ = msgIDs // caller's MO IDs — internal accounting only
	status, _ := deliverLocalCore(recipientMSISDN, senderMSISDN, segments, encoding, concatRef, numSegments)
	return status
}

func deliverLocalCore(recipientMSISDN, senderMSISDN string,
	segments []string, encoding string, concatRef *int, numSegments int) (string, []int64) {

	c := GetContext()
	recipient := lookupUEByMSISDN(recipientMSISDN)
	if recipient == nil {
		log.Warnf("MT-SMS: unknown recipient MSISDN=%s", recipientMSISDN)
		return "failed", nil
	}
	if !recipient.Enabled {
		return "failed", nil
	}
	recipientIMSI := recipient.IMSI

	// Create MT message records
	db, err := engine.Open()
	if err != nil {
		return "failed", nil
	}
	var mtMsgIDs []int64
	for _, segText := range segments {
		ref := 0
		if concatRef != nil {
			ref = *concatRef
		}
		result, err := db.Exec(
			`INSERT INTO sms_messages
			 (sender_imsi, sender_msisdn, recipient_msisdn, direction,
			  tp_da, tp_oa, tp_ud, encoding, status, segments, reference)
			 VALUES (?, ?, ?, 'MT', ?, ?, ?, ?, 'pending', ?, ?)`,
			recipientIMSI, senderMSISDN, recipientMSISDN,
			recipientMSISDN, senderMSISDN, segText, encoding, numSegments, ref)
		if err != nil {
			continue
		}
		id, _ := result.LastInsertId()
		mtMsgIDs = append(mtMsgIDs, id)
	}

	// Check reachability -- simplified (always reachable in current impl)
	// In production: check UeContextStore for active AMF context
	reachable := true

	if reachable {
		log.WithIMSI(recipientIMSI).Infof("MT-SMS: delivering to reachable UE (%d segments)", numSegments)
		c.setState(recipientIMSI, "mt_active")

		for i, segText := range segments {
			var udh []byte
			if numSegments > 1 {
				ref := 0
				if concatRef != nil {
					ref = *concatRef
				}
				udh = BuildConcatUDH(ref, numSegments, i+1, false)
			}
			tpdu := EncodeSMSDeliver(senderMSISDN, segText, encoding, udh)
			log.Debugf("MT-SMS TPDU[%d/%d]: %x", i+1, numSegments, tpdu)
		}

		now := float64(time.Now().Unix())
		for _, mid := range mtMsgIDs {
			_, _ = db.Exec("UPDATE sms_messages SET status='delivered', delivered_at=? WHERE id=?", now, mid)
		}
		c.setState(recipientIMSI, "idle")
		return "delivered", mtMsgIDs
	}

	// Store-and-forward
	for _, mid := range mtMsgIDs {
		c.queueMT(recipientIMSI, mid)
	}
	return "pending", mtMsgIDs
}

// SendMTSMS sends an MT-SMS to a UE (API / external SMSC entry point).
//
// `senderIMSI` is the originating subscriber's IMSI when the request
// came from a local UE (so the sms_messages.sender_imsi column gets
// the right value and /api/smsf/messages?imsi=… finds the row). Pass
// "" when the originator is external (PSTN/IMS-AS/SMSC) — the row
// is then tagged with the senderMSISDN only.
//
// Recipient handling per TS 23.040 §9.2.2.2 + TS 23.502 §4.13.3:
//   - Locally provisioned recipient → MT delivery path, row direction='MT'
//   - Non-local recipient           → MO record stored direction='MO'
//                                     status='pending' (would be routed
//                                     to an external SMSC in a real
//                                     deployment via routing rules)
func SendMTSMS(senderIMSI, senderMSISDN, recipientMSISDN, text, encoding string) MOResult {
	c := GetContext()
	if encoding == "gsm7" && !IsGSM7Encodable(text) {
		encoding = "ucs2"
	}
	segments := SegmentText(text, encoding)
	numSegments := len(segments)
	var concatRef *int
	if numSegments > 1 {
		ref := c.NextReference()
		concatRef = &ref
	}
	status, ids := deliverWithIDs(senderIMSI, senderMSISDN, recipientMSISDN,
		segments, encoding, concatRef, numSegments)
	return MOResult{
		OK:        status != "failed",
		MsgIDs:    ids,
		Status:    status,
		Segments:  numSegments,
		Encoding:  encoding,
		Reference: concatRef,
	}
}

// deliverWithIDs is the SendMTSMS / send-API entry point that
// distinguishes local-recipient (MT delivery path) from
// non-local-recipient (MO record stored as pending external). For
// local recipients it reuses the existing deliverLocalCore; for
// non-local it stores a single MO row chain so the
// /api/smsf/messages?imsi=<sender> query can find it (TS 23.040
// §9.2.2.2 SMS-SUBMIT preservation at the SMSF before external
// forwarding).
func deliverWithIDs(senderIMSI, senderMSISDN, recipientMSISDN string,
	segments []string, encoding string, concatRef *int, numSegments int) (string, []int64) {

	if recipient := lookupUEByMSISDN(recipientMSISDN); recipient != nil {
		// Local UE — MT path.
		return deliverLocalCore(recipientMSISDN, senderMSISDN, segments, encoding,
			concatRef, numSegments)
	}

	// Non-local — record as MO pending so the message is queryable
	// by sender IMSI and can be routed via SMSC. Per TS 23.040
	// §9.2.2.2 the SMS-SUBMIT is preserved at the SMSF.
	db, err := engine.Open()
	if err != nil {
		return "failed", nil
	}
	var ids []int64
	for _, segText := range segments {
		ref := 0
		if concatRef != nil {
			ref = *concatRef
		}
		result, err := db.Exec(
			`INSERT INTO sms_messages
			 (sender_imsi, sender_msisdn, recipient_msisdn, direction,
			  tp_da, tp_oa, tp_ud, encoding, status, segments, reference)
			 VALUES (?, ?, ?, 'MO', ?, ?, ?, ?, 'pending', ?, ?)`,
			senderIMSI, senderMSISDN, recipientMSISDN,
			recipientMSISDN, senderMSISDN, segText, encoding, numSegments, ref)
		if err != nil {
			continue
		}
		id, _ := result.LastInsertId()
		ids = append(ids, id)
	}
	log.Infof("MO-SMS: non-local recipient MSISDN=%s — %d segment(s) stored as pending (external route)",
		recipientMSISDN, numSegments)
	return "pending", ids
}

// DeliverPending delivers all pending MT-SMS queued for a UE.
func DeliverPending(imsi string) int {
	c := GetContext()
	db, err := engine.Open()
	if err != nil {
		return 0
	}
	delivered := 0
	for {
		msgID, ok := c.dequeueMT(imsi)
		if !ok {
			break
		}
		var tpUD, tpOA, enc string
		err := db.QueryRow(
			"SELECT tp_ud, tp_oa, encoding FROM sms_messages WHERE id=? AND status='pending'",
			msgID).Scan(&tpUD, &tpOA, &enc)
		if err != nil {
			continue
		}
		_ = EncodeSMSDeliver(tpOA, tpUD, enc, nil)
		now := float64(time.Now().Unix())
		_, _ = db.Exec("UPDATE sms_messages SET status='delivered', delivered_at=? WHERE id=?", now, msgID)
		delivered++
	}
	if delivered > 0 {
		log.WithIMSI(imsi).Infof("Delivered %d pending MT-SMS", delivered)
	}
	return delivered
}

// ================================================================
// Message expiry (TS 23.040 §9.2.3.12 TP-Validity-Period)
// ================================================================

const SMSExpirySeconds = 86400 * 3 // 3 days

// ExpireOldMessages marks old pending messages as expired.
func ExpireOldMessages() int {
	db, err := engine.Open()
	if err != nil {
		return 0
	}
	cutoff := float64(time.Now().Unix()) - SMSExpirySeconds
	result, err := db.Exec(
		"UPDATE sms_messages SET status='expired' WHERE status='pending' AND created_at < ?", cutoff)
	if err != nil {
		return 0
	}
	count, _ := result.RowsAffected()
	if count > 0 {
		log.Infof("Expired %d old pending SMS messages", count)
	}
	return int(count)
}

// ================================================================
// Query helpers (used by API)
// ================================================================

// GetMessages fetches SMS messages, optionally filtered by IMSI.
func GetMessages(imsi string, limit int) ([]map[string]any, error) {
	if limit <= 0 {
		limit = 50
	}
	db, err := engine.Open()
	if err != nil {
		return nil, err
	}

	var rows *sql.Rows
	if imsi != "" {
		rows, err = db.Query(
			`SELECT * FROM sms_messages
			 WHERE sender_imsi=? OR recipient_msisdn IN
			   (SELECT msisdn FROM ue WHERE imsi=?)
			 ORDER BY created_at DESC LIMIT ?`, imsi, imsi, limit)
	} else {
		rows, err = db.Query(
			"SELECT * FROM sms_messages ORDER BY created_at DESC LIMIT ?", limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanAllRows(rows)
}

// GetMessage fetches a single SMS message by ID.
func GetMessage(msgID int64) (map[string]any, error) {
	db, err := engine.Open()
	if err != nil {
		return nil, err
	}
	rows, err := db.Query("SELECT * FROM sms_messages WHERE id=?", msgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	results, _ := scanAllRows(rows)
	if len(results) == 0 {
		return nil, nil
	}
	return results[0], nil
}

// DeleteMessage deletes a single SMS message by ID.
func DeleteMessage(msgID int64) error {
	db, err := engine.Open()
	if err != nil {
		return err
	}
	_, err = db.Exec("DELETE FROM sms_messages WHERE id=?", msgID)
	return err
}

// GetRoutingRules fetches all SMS routing rules.
func GetRoutingRules() ([]map[string]any, error) {
	db, err := engine.Open()
	if err != nil {
		return nil, err
	}
	rows, err := db.Query("SELECT * FROM sms_routing ORDER BY priority DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanAllRows(rows)
}

// AddRoutingRule adds an SMS routing rule.
func AddRoutingRule(msisdnPattern, routeType, destination string, priority int) (map[string]any, error) {
	if routeType != "local" && routeType != "smsc" && routeType != "forward" {
		return map[string]any{"ok": false, "error": "Invalid route_type"}, nil
	}
	db, err := engine.Open()
	if err != nil {
		return nil, err
	}
	result, err := db.Exec(
		"INSERT INTO sms_routing (msisdn_pattern, route_type, destination, priority) VALUES (?, ?, ?, ?)",
		msisdnPattern, routeType, destination, priority)
	if err != nil {
		return nil, err
	}
	id, _ := result.LastInsertId()
	return map[string]any{"ok": true, "id": id}, nil
}

// DeleteRoutingRule deletes an SMS routing rule by ID.
func DeleteRoutingRule(ruleID int64) error {
	db, err := engine.Open()
	if err != nil {
		return err
	}
	_, err = db.Exec("DELETE FROM sms_routing WHERE id=?", ruleID)
	return err
}

// GetStats returns SMS message counts grouped by status.
func GetStats() map[string]any {
	stats := map[string]any{
		"pending": 0, "delivered": 0, "failed": 0, "expired": 0, "total": 0,
	}
	db, err := engine.Open()
	if err != nil {
		return stats
	}
	rows, err := db.Query("SELECT status, COUNT(*) as cnt FROM sms_messages GROUP BY status")
	if err != nil {
		return stats
	}
	defer rows.Close()
	total := 0
	for rows.Next() {
		var status string
		var cnt int
		if err := rows.Scan(&status, &cnt); err != nil {
			continue
		}
		stats[status] = cnt
		total += cnt
	}
	stats["total"] = total
	stats["active_sessions"] = GetContext().GetSessionCount()
	return stats
}

// ================================================================
// helpers
// ================================================================

func scanAllRows(rows *sql.Rows) ([]map[string]any, error) {
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	var out []map[string]any
	for rows.Next() {
		scan := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range scan {
			ptrs[i] = &scan[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			continue
		}
		row := make(map[string]any, len(cols))
		for i, name := range cols {
			row[name] = scan[i]
		}
		out = append(out, row)
	}
	return out, nil
}

func init() {
	// Ensure fmt is used.
	_ = fmt.Sprintf
}
