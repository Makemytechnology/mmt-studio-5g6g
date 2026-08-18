// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// db/seed/manifest.go — declarative baseline manifest loader.
//
// db/seed/baseline.yaml is the single source of truth for the baseline DB
// state. The Go seeders read it via LoadBaseline() instead of hard-coding
// counts, IMSIs, AMBR values, slice IDs, and KDF parameters. Bumping a
// number in the YAML is the only edit needed; the seeders pick it up on
// next rebuild.
//
// Migration status — domains driven from the manifest today:
//   ✅ UE roster + auth + subscription   (db/seed/ue.go)
//   ⏳ PLMN / GUAMI                       (db/seed/plmn.go — TODO)
//   ⏳ DNN catalog                        (db/seed/network.go — TODO)
//   ⏳ UPF anchors                        (db/seed/upf.go — TODO)
//   ⏳ IMS subscribers                    (db/seed/ims.go — TODO)
//   ⏳ QoS 5QI catalog                    (db/seed/services.go — TODO)
//
// Each remaining domain follows the same pattern: replace hard-coded
// constants in the *.go seeder with values from m := LoadBaseline().

package seed

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"fmt"
	"strconv"

	"gopkg.in/yaml.v3"
)

//go:embed baseline.yaml
var baselineYAML []byte

// Manifest is the parsed form of baseline.yaml. Fields not yet consumed
// by Go seeders are still defined so YAML validation catches typos and so
// future seeders have a stable struct shape to read from.
type Manifest struct {
	Name        string `yaml:"name"`
	Version     string `yaml:"version"`
	Description string `yaml:"description"`

	PLMN struct {
		MCC string `yaml:"mcc"`
		MNC string `yaml:"mnc"`
	} `yaml:"plmn"`

	AMF struct {
		GUAMI struct {
			RegionID int `yaml:"region_id"`
			SetID    int `yaml:"set_id"`
			Pointer  int `yaml:"pointer"`
		} `yaml:"guami"`
		NGAPBind string `yaml:"ngap_bind"`
	} `yaml:"amf"`

	Slices []Slice `yaml:"slices"`
	DNNs   []DNN   `yaml:"dnns"`
	UPFs   []UPF   `yaml:"upfs"`

	QoS5QICatalog []QoS5QI `yaml:"qos_5qi_catalog"`

	Security struct {
		CipheringEnabled    []string `yaml:"ciphering_enabled"`
		IntegrityEnabled    []string `yaml:"integrity_enabled"`
		NullCipherEnabled   bool     `yaml:"null_cipher_enabled"`
		AuthMethods         []string `yaml:"authentication_methods"`
	} `yaml:"security"`

	UECredentials UECredentials `yaml:"ue_credentials"`
	UEBuckets     []UEBucket    `yaml:"ue_buckets"`

	IMSSubscribers struct {
		IMSIStart    string `yaml:"imsi_start"`
		Count        int    `yaml:"count"`
		Realm        string `yaml:"realm"`
		IMPITemplate string `yaml:"impi_template"`
		IMPUTemplate string `yaml:"impu_template"`
	} `yaml:"ims_subscribers"`
}

type Slice struct {
	SST         int    `yaml:"sst"`
	SD          string `yaml:"sd"`
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
}

type DNN struct {
	Name              string `yaml:"name"`
	Pool              string `yaml:"pool"`
	Default5QI        int    `yaml:"default_5qi"`
	SessionAmbrDLKbps int    `yaml:"session_ambr_dl_kbps"`
	SessionAmbrULKbps int    `yaml:"session_ambr_ul_kbps"`
	Slices            []int  `yaml:"slices"`
}

type UPF struct {
	ID             string   `yaml:"id"`
	Address        string   `yaml:"address"`
	Port           int      `yaml:"port"`
	SupportedSST   []int    `yaml:"supported_sst"`
	SupportedDNNs  []string `yaml:"supported_dnns"`
}

type QoS5QI struct {
	ID           int     `yaml:"id"`
	ResourceType string  `yaml:"resource_type"`
	Priority     int     `yaml:"priority"`
	PDBMs        int     `yaml:"pdb_ms"`
	PELR         float64 `yaml:"pelr"`
	Description  string  `yaml:"description"`
}

// UECredentials controls how K and OPc are derived for every seeded UE.
// Two modes are supported by the schema, with mode auto-detected by which
// fields are present:
//
//   • Deterministic per-IMSI (preferred). Both `k_derivation` and
//     `opc_derivation` are set, plus `kdf_version`. K = first 16 bytes of
//     sha256(imsi || "MMT-K-" || kdf_version). OPc same with "MMT-OPc-".
//     Rotating kdf_version rolls every UE's creds at once. Tester-side
//     sim_db.json must derive identically.
//
//   • Static (legacy). `k` and `opc` are set directly. Every UE gets the
//     same K/OPc. Kept so a v1-version manifest can still seed.
type UECredentials struct {
	KDFVersion     string `yaml:"kdf_version"`
	KDerivation    string `yaml:"k_derivation"`
	OPcDerivation  string `yaml:"opc_derivation"`
	StaticK        string `yaml:"k"`
	StaticOPc      string `yaml:"opc"`
	AMF            string `yaml:"amf"`
	InitialSQN     int    `yaml:"initial_sqn"`
}

// UEBucket is a contiguous IMSI range with shared subscription attributes.
// Buckets must be non-overlapping (validated on load).
type UEBucket struct {
	Name           string   `yaml:"name"`
	Count          int      `yaml:"count"`
	IMSIStart      string   `yaml:"imsi_start"`
	MSISDNStart    string   `yaml:"msisdn_start"`
	Slices         []int    `yaml:"slices"`
	DNNs           []string `yaml:"dnns"`
	DefaultDNN     string   `yaml:"default_dnn"`
	UEAmbrDLKbps   int      `yaml:"ue_ambr_dl_kbps"`
	UEAmbrULKbps   int      `yaml:"ue_ambr_ul_kbps"`
}

// LoadBaseline parses the embedded baseline.yaml. Cached after the first
// call because the file content is fixed at build time.
var loadedBaseline *Manifest

func LoadBaseline() (*Manifest, error) {
	if loadedBaseline != nil {
		return loadedBaseline, nil
	}
	var m Manifest
	if err := yaml.Unmarshal(baselineYAML, &m); err != nil {
		return nil, fmt.Errorf("parse baseline.yaml: %w", err)
	}
	if err := validateManifest(&m); err != nil {
		return nil, fmt.Errorf("validate baseline.yaml: %w", err)
	}
	loadedBaseline = &m
	return &m, nil
}

func validateManifest(m *Manifest) error {
	if m.Name == "" || m.Version == "" {
		return fmt.Errorf("name and version are required")
	}
	// UE credentials must specify exactly one mode (derived OR static).
	derived := m.UECredentials.KDerivation != "" && m.UECredentials.OPcDerivation != ""
	static := m.UECredentials.StaticK != "" && m.UECredentials.StaticOPc != ""
	if derived == static {
		return fmt.Errorf("ue_credentials: specify EITHER (k_derivation+opc_derivation+kdf_version) OR (k+opc), not both/neither")
	}
	if derived && m.UECredentials.KDFVersion == "" {
		return fmt.Errorf("ue_credentials.kdf_version required when using derivation")
	}
	// UE buckets must be non-overlapping. Ranges are large integers
	// (IMSI is 15 digits; uint64 holds it).
	type rng struct {
		name string
		lo, hi uint64
	}
	var ranges []rng
	for _, b := range m.UEBuckets {
		if b.Count <= 0 {
			return fmt.Errorf("ue_bucket %q: count must be > 0", b.Name)
		}
		start, err := strconv.ParseUint(b.IMSIStart, 10, 64)
		if err != nil {
			return fmt.Errorf("ue_bucket %q: imsi_start %q not a 15-digit integer: %w", b.Name, b.IMSIStart, err)
		}
		ranges = append(ranges, rng{name: b.Name, lo: start, hi: start + uint64(b.Count) - 1})
	}
	for i := 0; i < len(ranges); i++ {
		for j := i + 1; j < len(ranges); j++ {
			a, b := ranges[i], ranges[j]
			if a.lo <= b.hi && b.lo <= a.hi {
				return fmt.Errorf("ue_buckets %q and %q overlap (%d..%d vs %d..%d)",
					a.name, b.name, a.lo, a.hi, b.lo, b.hi)
			}
		}
	}
	return nil
}

// DeriveK returns the per-IMSI K key (32-hex-character / 16-byte) for the
// given IMSI under the manifest's KDF, or the static K if the manifest is
// in static mode. The same derivation is implemented in the tester's
// config/sim_db_gen.py — both sides must agree byte-for-byte for 5G-AKA
// to succeed.
//
//   derived: K = hex(sha256(imsi || "MMT-K-" || kdf_version)[:16])
func (m *Manifest) DeriveK(imsi string) string {
	if m.UECredentials.StaticK != "" {
		return m.UECredentials.StaticK
	}
	h := sha256.Sum256([]byte(imsi + "MMT-K-" + m.UECredentials.KDFVersion))
	return hex.EncodeToString(h[:16])
}

// DeriveOPc returns the per-IMSI OPc value matching DeriveK.
//   derived: OPc = hex(sha256(imsi || "MMT-OPc-" || kdf_version)[:16])
func (m *Manifest) DeriveOPc(imsi string) string {
	if m.UECredentials.StaticOPc != "" {
		return m.UECredentials.StaticOPc
	}
	h := sha256.Sum256([]byte(imsi + "MMT-OPc-" + m.UECredentials.KDFVersion))
	return hex.EncodeToString(h[:16])
}

// IMSI returns the Nth IMSI in this bucket (0-indexed). The bucket's
// IMSIStart is parsed as a 15-digit integer, idx added, and the result
// zero-padded back to 15 digits.
func (b *UEBucket) IMSI(idx int) string {
	start, _ := strconv.ParseUint(b.IMSIStart, 10, 64)
	return fmt.Sprintf("%015d", start+uint64(idx))
}

// MSISDN returns the Nth MSISDN in this bucket. The MSISDN field is
// treated the same way as IMSI but its width matches the declared start
// length (typically 10 digits — TS 23.003 §3.3 E.164 sans country code).
func (b *UEBucket) MSISDN(idx int) string {
	start, _ := strconv.ParseUint(b.MSISDNStart, 10, 64)
	return fmt.Sprintf("%0*d", len(b.MSISDNStart), start+uint64(idx))
}
