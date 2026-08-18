// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// RESTCONF (RFC 8040) intake for the IETF DetNet YANG model
// (RFC 9633) — the standards-track controller interface of TS 23.501
// §5.28.5.3: "the TSCTSF receives the DetNet YANG configuration ...
// via Netconf/Restconf". This is the RESTCONF binding; NETCONF over
// SSH (RFC 6242) is intentionally NOT implemented — controllers use
// this HTTP surface.
//
// Implemented per RFC 8040:
//
//	§3.1  /.well-known/host-meta        → XRD root discovery
//	§3.3  {+restconf}                   → API resource (data/operations)
//	§3.5  Media type application/yang-data+json
//	§4    Methods on the datastore resource:
//	        GET    /restconf/data/ietf-detnet:detnet
//	        POST   /restconf/data/ietf-detnet:detnet/app-flows
//	        PUT    /restconf/data/ietf-detnet:detnet/app-flows/app-flow={name}
//	        DELETE /restconf/data/ietf-detnet:detnet/app-flows/app-flow={name}
//	§7.1  "ietf-restconf:errors" error format
//
// YANG-JSON mapping (RFC 7951): uint64 leafs encode as strings;
// identifiers are namespace-qualified at the top of each subtree.
// The 3GPP DetNet extensions of TS 23.503 §6.1.3.23b ride the
// "mmt-3gpp-detnet:*" augmentation names (5gs-node-max-latency,
// max-consecutive-loss-tolerance, target-supi, target-dnn — the
// latter two are the studio's stand-in for the §5.28.5.3 source-IP →
// UE resolution).
package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/mmt/mmt-studio-core/nf/tsctsf"
)

const yangDataJSON = "application/yang-data+json"

// restconfError writes an RFC 8040 §7.1 errors document.
func restconfError(w http.ResponseWriter, status int, errType, tag, msg string) {
	w.Header().Set("Content-Type", yangDataJSON)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ietf-restconf:errors": map[string]any{
			"error": []map[string]any{{
				"error-type":    errType,
				"error-tag":     tag,
				"error-message": msg,
			}},
		},
	})
}

func restconfReply(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", yangDataJSON)
	w.WriteHeader(status)
	if body != nil {
		_ = json.NewEncoder(w).Encode(body)
	}
}

// yangAppFlow is the RFC 9633 app-flow subtree in YANG-JSON form
// (RFC 7951 encoding: uint64 → string).
type yangAppFlow struct {
	Name                 string        `json:"name"`
	TrafficSpecification *yangTrafSpec `json:"traffic-specification,omitempty"`
	// RFC 9633 ip-flow identification (data-flow-spec subset).
	IPFlow *yangIPFlow `json:"ip-flow,omitempty"`
	// 3GPP augmentations (TS 23.503 §6.1.3.23b).
	Node5GSMaxLatency uint64 `json:"mmt-3gpp-detnet:5gs-node-max-latency,omitempty"` // microseconds
	MaxConsecLossTol  int    `json:"mmt-3gpp-detnet:max-consecutive-loss-tolerance,omitempty"`
	TargetSUPI        string `json:"mmt-3gpp-detnet:target-supi,omitempty"`
	TargetDNN         string `json:"mmt-3gpp-detnet:target-dnn,omitempty"`
	FlowDirection     string `json:"mmt-3gpp-detnet:flow-direction,omitempty"` // "UL"|"DL"
}

type yangTrafSpec struct {
	IntervalUS         uint64 `json:"interval"` // microseconds (RFC 9633)
	MaxPktsPerInterval int    `json:"max-pkts-per-interval,omitempty"`
	MaxPayloadSize     int    `json:"max-payload-size,omitempty"`
	// uint64 in YANG-JSON is a string (RFC 7951 §6.1).
	MinBandwidth string `json:"min-bandwidth,omitempty"` // bits/second
}

type yangIPFlow struct {
	SrcIPPrefix  string `json:"src-ip-prefix,omitempty"`
	DestIPPrefix string `json:"dest-ip-prefix,omitempty"`
	Protocol     int    `json:"protocol,omitempty"`
	SourcePort   int    `json:"source-port,omitempty"`
	DestPort     int    `json:"destination-port,omitempty"`
}

// toDetNetFlow maps the YANG subtree onto the TSCTSF flow model.
func (y *yangAppFlow) toDetNetFlow() (*tsctsf.DetNetFlow, error) {
	if y.Name == "" {
		return nil, fmt.Errorf("app-flow name is mandatory (RFC 9633)")
	}
	if y.TrafficSpecification == nil {
		return nil, fmt.Errorf("traffic-specification is mandatory")
	}
	f := &tsctsf.DetNetFlow{
		Name:                     y.Name,
		IntervalUS:               y.TrafficSpecification.IntervalUS,
		MaxPktsPerInterval:       y.TrafficSpecification.MaxPktsPerInterval,
		MaxPayloadSize:           y.TrafficSpecification.MaxPayloadSize,
		Node5GSMaxLatencyUS:      y.Node5GSMaxLatency,
		MaxConsecutiveLossTolNum: y.MaxConsecLossTol,
		SUPI:                     y.TargetSUPI,
		DNN:                      y.TargetDNN,
		FlowDirection:            y.FlowDirection,
	}
	if bw := y.TrafficSpecification.MinBandwidth; bw != "" {
		v, err := strconv.ParseUint(bw, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("min-bandwidth %q: %v", bw, err)
		}
		f.MinBandwidthBps = v
	}
	if ip := y.IPFlow; ip != nil {
		f.SrcIP, f.DstIP = ip.SrcIPPrefix, ip.DestIPPrefix
		f.Protocol, f.SrcPort, f.DstPort = ip.Protocol, ip.SourcePort, ip.DestPort
	}
	return f, nil
}

func yangFromDetNetFlow(f *tsctsf.DetNetFlow) *yangAppFlow {
	y := &yangAppFlow{
		Name: f.Name,
		TrafficSpecification: &yangTrafSpec{
			IntervalUS:         f.IntervalUS,
			MaxPktsPerInterval: f.MaxPktsPerInterval,
			MaxPayloadSize:     f.MaxPayloadSize,
		},
		Node5GSMaxLatency: f.Node5GSMaxLatencyUS,
		MaxConsecLossTol:  f.MaxConsecutiveLossTolNum,
		TargetSUPI:        f.SUPI,
		TargetDNN:         f.DNN,
		FlowDirection:     f.FlowDirection,
	}
	if f.MinBandwidthBps > 0 {
		y.TrafficSpecification.MinBandwidth = strconv.FormatUint(f.MinBandwidthBps, 10)
	}
	if f.SrcIP != "" || f.DstIP != "" || f.Protocol > 0 {
		y.IPFlow = &yangIPFlow{
			SrcIPPrefix: f.SrcIP, DestIPPrefix: f.DstIP,
			Protocol: f.Protocol, SourcePort: f.SrcPort, DestPort: f.DstPort,
		}
	}
	return y
}

// decodeAppFlow accepts either the bare object or the RFC 7951
// namespaced wrapper {"ietf-detnet:app-flow": [ {...} ]} / single.
func decodeAppFlow(r *http.Request) (*yangAppFlow, error) {
	var raw map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		return nil, err
	}
	payload, ok := raw["ietf-detnet:app-flow"]
	if !ok {
		if payload, ok = raw["app-flow"]; !ok {
			// Bare object: re-marshal the whole map.
			b, _ := json.Marshal(raw)
			payload = b
		}
	}
	var y yangAppFlow
	if err := json.Unmarshal(payload, &y); err != nil {
		// RFC 7951 lists encode as arrays — accept [ {...} ].
		var list []yangAppFlow
		if err2 := json.Unmarshal(payload, &list); err2 != nil || len(list) != 1 {
			return nil, err
		}
		y = list[0]
	}
	return &y, nil
}

func (s *Server) registerRESTCONFRoutes() {
	r := s.Router

	// RFC 8040 §3.1 root discovery.
	r.Get("/.well-known/host-meta", func(w http.ResponseWriter, rq *http.Request) {
		w.Header().Set("Content-Type", "application/xrd+xml")
		_, _ = w.Write([]byte(`<XRD xmlns='http://docs.oasis-open.org/ns/xri/xrd-1.0'>` +
			`<Link rel='restconf' href='/restconf'/></XRD>`))
	})

	// RFC 8040 §3.3 API resource.
	r.Get("/restconf", func(w http.ResponseWriter, rq *http.Request) {
		restconfReply(w, http.StatusOK, map[string]any{
			"ietf-restconf:restconf": map[string]any{
				"data":                 map[string]any{},
				"operations":           map[string]any{},
				"yang-library-version": "2019-01-04",
			},
		})
	})

	// GET the DetNet subtree (RFC 9633 app-flows).
	r.Get("/restconf/data/ietf-detnet:detnet", func(w http.ResponseWriter, rq *http.Request) {
		flows := tsctsf.Default.DetNetFlows()
		list := make([]*yangAppFlow, 0, len(flows))
		for _, f := range flows {
			list = append(list, yangFromDetNetFlow(f))
		}
		restconfReply(w, http.StatusOK, map[string]any{
			"ietf-detnet:detnet": map[string]any{
				"app-flows": map[string]any{"app-flow": list},
			},
		})
	})

	// POST — create an app-flow (RFC 8040 §4.4.1: POST to the parent).
	r.Post("/restconf/data/ietf-detnet:detnet/app-flows", func(w http.ResponseWriter, rq *http.Request) {
		y, err := decodeAppFlow(rq)
		if err != nil {
			restconfError(w, http.StatusBadRequest, "protocol", "malformed-message", err.Error())
			return
		}
		f, err := y.toDetNetFlow()
		if err != nil {
			restconfError(w, http.StatusBadRequest, "application", "invalid-value", err.Error())
			return
		}
		if err := tsctsf.Default.DetNetFlowCreate(f); err != nil {
			restconfError(w, http.StatusConflict, "application", "resource-denied", err.Error())
			return
		}
		w.Header().Set("Location",
			"/restconf/data/ietf-detnet:detnet/app-flows/app-flow="+f.Name)
		restconfReply(w, http.StatusCreated, nil)
	})

	// PUT — create-or-replace one app-flow (RFC 8040 §4.5).
	r.Put("/restconf/data/ietf-detnet:detnet/app-flows/app-flow={name}", func(w http.ResponseWriter, rq *http.Request) {
		name := chi.URLParam(rq, "name")
		y, err := decodeAppFlow(rq)
		if err != nil {
			restconfError(w, http.StatusBadRequest, "protocol", "malformed-message", err.Error())
			return
		}
		if y.Name == "" {
			y.Name = name
		}
		if y.Name != name {
			restconfError(w, http.StatusBadRequest, "protocol", "invalid-value",
				"app-flow name does not match resource key")
			return
		}
		f, err := y.toDetNetFlow()
		if err != nil {
			restconfError(w, http.StatusBadRequest, "application", "invalid-value", err.Error())
			return
		}
		replaced := tsctsf.Default.DetNetFlowDelete(name) == nil
		if err := tsctsf.Default.DetNetFlowCreate(f); err != nil {
			restconfError(w, http.StatusConflict, "application", "resource-denied", err.Error())
			return
		}
		if replaced {
			restconfReply(w, http.StatusNoContent, nil)
		} else {
			restconfReply(w, http.StatusCreated, nil)
		}
	})

	// DELETE one app-flow (RFC 8040 §4.7).
	r.Delete("/restconf/data/ietf-detnet:detnet/app-flows/app-flow={name}", func(w http.ResponseWriter, rq *http.Request) {
		if err := tsctsf.Default.DetNetFlowDelete(chi.URLParam(rq, "name")); err != nil {
			restconfError(w, http.StatusNotFound, "application", "data-missing", err.Error())
			return
		}
		restconfReply(w, http.StatusNoContent, nil)
	})
}
