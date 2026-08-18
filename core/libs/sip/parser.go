// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// SIP parser — RFC 3261 message parsing with header folding + multi-value support.
package sip

import (
	"fmt"
	"strconv"
	"strings"
)

// noCommaSplit headers that contain commas in values and must not be split.
var noCommaSplit = map[string]struct{}{
	"www-authenticate":       {},
	"authorization":          {},
	"proxy-authenticate":     {},
	"proxy-authorization":    {},
}

// Parse converts raw SIP bytes into a *SipRequest or *SipResponse.
func Parse(data []byte) (any, error) {
	text := string(data)
	parts := strings.SplitN(text, "\r\n\r\n", 2)
	headerSection := parts[0]
	body := ""
	if len(parts) > 1 {
		body = parts[1]
	}

	lines := strings.Split(headerSection, "\r\n")
	if len(lines) == 0 {
		return nil, fmt.Errorf("sip: empty message")
	}

	// Header folding (RFC 3261 §7.3.1).
	var unfolded []string
	for _, line := range lines {
		if len(line) > 0 && (line[0] == ' ' || line[0] == '\t') && len(unfolded) > 0 {
			unfolded[len(unfolded)-1] += " " + strings.TrimSpace(line)
		} else {
			unfolded = append(unfolded, line)
		}
	}

	startLine := unfolded[0]
	headers := make(map[string][]string)
	for _, hl := range unfolded[1:] {
		idx := strings.Index(hl, ":")
		if idx < 0 {
			continue
		}
		name := strings.TrimSpace(hl[:idx])
		value := strings.TrimSpace(hl[idx+1:])
		lower := strings.ToLower(name)
		if _, skip := noCommaSplit[lower]; skip || lower == "via" || lower == "v" {
			headers[name] = append(headers[name], value)
		} else {
			for _, v := range strings.Split(value, ",") {
				headers[name] = append(headers[name], strings.TrimSpace(v))
			}
		}
	}

	if strings.HasPrefix(startLine, "SIP/2.0") {
		// Response: "SIP/2.0 200 OK"
		fields := strings.SplitN(startLine, " ", 3)
		if len(fields) < 2 {
			return nil, fmt.Errorf("sip: bad response line: %q", startLine)
		}
		code, err := strconv.Atoi(fields[1])
		if err != nil {
			return nil, fmt.Errorf("sip: bad status code: %q", fields[1])
		}
		reason := ""
		if len(fields) > 2 {
			reason = fields[2]
		}
		return &SipResponse{
			SipMessage: SipMessage{Headers: headers, Body: body},
			StatusCode: code,
			Reason:     reason,
		}, nil
	}

	// Request: "REGISTER sip:ims.mnc001.mcc001.3gppnetwork.org SIP/2.0"
	fields := strings.SplitN(startLine, " ", 3)
	if len(fields) < 2 {
		return nil, fmt.Errorf("sip: bad request line: %q", startLine)
	}
	return &SipRequest{
		SipMessage: SipMessage{Headers: headers, Body: body},
		Method:     fields[0],
		RequestURI: fields[1],
	}, nil
}
