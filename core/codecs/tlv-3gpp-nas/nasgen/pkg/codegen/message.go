// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
package codegen

import (
	"github.com/dave/jennifer/jen"
	"github.com/mmt/nasgen/pkg/schema"
)

// generateMessage emits the message struct and its Decode / Encode methods.
//
// Layout of payload passed to Decode (caller strips EPD + SHT + msgType):
//
//	[mandatory IE bytes in spec order][optional IEs in any order, IEI-dispatched]
//
// Mandatory IEs with format V and half-octet length ("1/2") are expected to
// appear in adjacent pairs packed into a single byte; the code generator
// detects consecutive half-octet IEs and emits a paired read.
func (g *Generator) generateMessage(f *jen.File, m schema.MessageDef) {
	typeName := GoName(m.Name)

	// Message-type constant
	f.Const().Id("MessageType" + typeName).Op("=").Lit(int(m.MessageType))

	// struct
	f.Commentf("// %s — %s", typeName, m.Description)
	f.Type().Id(typeName).StructFunc(func(grp *jen.Group) {
		// 5GSM wire header carries per-message PDU session id + procedure
		// transaction id (TS 24.501 §9.4). Surface them as struct fields.
		if m.EPD == 0x2E {
			grp.Id("PDUSessionID").Uint8()
			grp.Id("PTI").Uint8().Commentf("procedure transaction identity")
		}
		// LTE ESM (TS 24.301 §9.2): first octet high nibble is EPS Bearer
		// Identity, octet 2 is PTI, octet 3 is msg type.
		if m.EPD == 0x02 {
			grp.Id("EPSBearerIdentity").Uint8().Commentf("4 bits; 0 = none")
			grp.Id("PTI").Uint8().Commentf("procedure transaction identity")
		}
		for _, ie := range m.IEs {
			fieldType := g.ieFieldGoType(ie)
			if ie.IsMandatory() || g.ieIsInterface(ie) {
				// interface types are already nil-able — don't wrap in pointer
				grp.Id(GoName(ie.Name)).Add(fieldType)
			} else {
				// optional value types → pointer so nil means "absent"
				grp.Id(GoName(ie.Name)).Op("*").Add(fieldType)
			}
		}
	})

	g.emitMessageEncode(f, m, typeName)
	g.emitMessageDecode(f, m, typeName)
}

// ieFieldGoType maps an IE entry to its Go field type (the referenced IE type).
func (g *Generator) ieFieldGoType(ie schema.IEEntry) *jen.Statement {
	return jen.Id(GoName(ie.TypeRef))
}

// ieIsInterface returns true when the referenced IE type is a runtime
// interface alias (it's inherently nil-able — no pointer wrapper needed
// for optional fields).
func (g *Generator) ieIsInterface(ie schema.IEEntry) bool {
	t, ok := g.Repo.IETypes[ie.TypeRef]
	if !ok {
		return false
	}
	switch t.GoType {
	case "MobileIdentity5GS":
		return true
	}
	return false
}

// pairIndex returns i+1 if IE at i and i+1 are both mandatory half-octet V IEs.
// Otherwise returns -1.
func pairIndex(ies []schema.IEEntry, i int) int {
	if i+1 >= len(ies) {
		return -1
	}
	a, b := ies[i], ies[i+1]
	if a.IsMandatory() && b.IsMandatory() &&
		a.Format == "V" && b.Format == "V" &&
		a.Length == "1/2" && b.Length == "1/2" {
		return i + 1
	}
	return -1
}

// --- Encode ---

func (g *Generator) emitMessageEncode(f *jen.File, m schema.MessageDef, typeName string) {
	f.Func().Params(jen.Id("m").Op("*").Id(typeName)).
		Id("Encode").Params().Params(jen.Index().Byte(), jen.Error()).
		BlockFunc(func(grp *jen.Group) {
			grp.Id("e").Op(":=").Add(g.qualRuntime("NewNASEncoder")).Call()

			// Header: EPD + SHT/PDU-session-id + msgType.
			// For 5GMM: EPD, SHT=0 + spare(4), msgType.
			// For 5GSM: EPD, PDU session identity, procedure transaction id, msgType.
			// We emit the minimal common header (EPD, 0x00, msgType). Full 5GSM
			// header handling is stubbed — tests so far exercise 5GMM.
			switch m.EPD {
			case 0x7E:
				// Plain 5GMM: EPD, SHT=0, msgType.
				grp.Id("_").Op("=").Id("e").Dot("WriteByte").Call(jen.Lit(0x7E))
				grp.Id("_").Op("=").Id("e").Dot("WriteByte").Call(jen.Lit(0))
				grp.Id("_").Op("=").Id("e").Dot("WriteByte").Call(jen.Lit(int(m.MessageType)))
			case 0x2E:
				// 5GSM: EPD, PDU session id, PTI, msgType.
				grp.Id("_").Op("=").Id("e").Dot("WriteByte").Call(jen.Lit(0x2E))
				grp.Id("_").Op("=").Id("e").Dot("WriteByte").Call(jen.Id("m").Dot("PDUSessionID"))
				grp.Id("_").Op("=").Id("e").Dot("WriteByte").Call(jen.Id("m").Dot("PTI"))
				grp.Id("_").Op("=").Id("e").Dot("WriteByte").Call(jen.Lit(int(m.MessageType)))
			case 0x07:
				// LTE EMM plain: (SHT<<4 | PD) with SHT=0, then msgType.
				grp.Id("_").Op("=").Id("e").Dot("WriteByte").Call(jen.Lit(0x07))
				grp.Id("_").Op("=").Id("e").Dot("WriteByte").Call(jen.Lit(int(m.MessageType)))
			case 0x02:
				// LTE ESM: (EBI<<4 | 0x02), PTI, msgType.
				grp.Id("_").Op("=").Id("e").Dot("WriteByte").Call(
					jen.Parens(jen.Id("m").Dot("EPSBearerIdentity").Op("&").Lit(0x0F)).Op("<<").Lit(4).
						Op("|").Lit(0x02))
				grp.Id("_").Op("=").Id("e").Dot("WriteByte").Call(jen.Id("m").Dot("PTI"))
				grp.Id("_").Op("=").Id("e").Dot("WriteByte").Call(jen.Lit(int(m.MessageType)))
			default:
				grp.Id("_").Op("=").Id("e").Dot("WriteByte").Call(jen.Lit(int(m.EPD)))
				grp.Id("_").Op("=").Id("e").Dot("WriteByte").Call(jen.Lit(0))
				grp.Id("_").Op("=").Id("e").Dot("WriteByte").Call(jen.Lit(int(m.MessageType)))
			}

			// Mandatory IEs in spec order
			i := 0
			for i < len(m.IEs) {
				ie := m.IEs[i]
				if !ie.IsMandatory() {
					i++
					continue
				}
				if pi := pairIndex(m.IEs, i); pi != -1 {
					g.emitHalfOctetPairEncode(grp, ie, m.IEs[pi])
					i = pi + 1
					continue
				}
				g.emitMandatoryEncode(grp, ie)
				i++
			}

			// Optional IEs — emit in spec order if present.
			for _, ie := range m.IEs {
				if ie.IsMandatory() {
					continue
				}
				g.emitOptionalEncode(grp, ie)
			}

			grp.Return(jen.Id("e").Dot("Bytes").Call(), jen.Nil())
		})
}

func (g *Generator) emitHalfOctetPairEncode(grp *jen.Group, low, high schema.IEEntry) {
	// Pack low.Encode() (bits 0-3) with high.Encode() (bits 4-7) into one byte.
	grp.BlockFunc(func(blk *jen.Group) {
		blk.Id("lo").Op(":=").Id("m").Dot(GoName(low.Name)).Dot("Encode").Call().Op("&").Lit(0x0F)
		blk.Id("hi").Op(":=").Id("m").Dot(GoName(high.Name)).Dot("Encode").Call().Op("&").Lit(0x0F)
		blk.Id("_").Op("=").Id("e").Dot("WriteByte").Call(jen.Parens(jen.Id("hi").Op("<<").Lit(4)).Op("|").Id("lo"))
	})
}

func (g *Generator) emitMandatoryEncode(grp *jen.Group, ie schema.IEEntry) {
	ref := "m." + GoName(ie.Name)
	ieType := g.Repo.IETypes[ie.TypeRef]

	switch ie.Format {
	case "V":
		if ieNeedsBitFieldByte(ieType) {
			grp.Id("_").Op("=").Id("e").Dot("WriteByte").Call(jen.Id(ref).Dot("Encode").Call())
		} else if ieType.GoType != "" {
			// runtime-typed — expect Encode() []byte
			grp.Id("_").Op("=").Id("e").Dot("WriteBytes").Call(jen.Id(ref).Dot("Encode").Call())
		} else {
			grp.Id("_").Op("=").Id("e").Dot("WriteBytes").Call(jen.Id(ref).Dot("EncodeBytes").Call())
		}
	case "LV":
		if ieType.GoType != "" {
			grp.Id("_").Op("=").Id("e").Dot("EncodeLV").Call(jen.Id(ref).Dot("Encode").Call())
		} else {
			grp.Id("_").Op("=").Id("e").Dot("EncodeLV").Call(jen.Id(ref).Dot("EncodeBytes").Call())
		}
	case "LV-E":
		if ieType.GoType != "" {
			grp.Id("_").Op("=").Id("e").Dot("EncodeLVE").Call(jen.Id(ref).Dot("Encode").Call())
		} else {
			grp.Id("_").Op("=").Id("e").Dot("EncodeLVE").Call(jen.Id(ref).Dot("EncodeBytes").Call())
		}
	default:
		grp.Comment("// unsupported mandatory format " + ie.Format + " for IE " + ie.Name)
	}
}

func (g *Generator) emitOptionalEncode(grp *jen.Group, ie schema.IEEntry) {
	ref := "m." + GoName(ie.Name)
	ieType := g.Repo.IETypes[ie.TypeRef]
	iei := *ie.IEI

	grp.If(jen.Id(ref).Op("!=").Nil()).BlockFunc(func(blk *jen.Group) {
		ieiByte, half, _ := schema.ParseIEI(iei)
		switch ie.Format {
		case "TV":
			if half {
				// Type 1 TV: IEI high nibble, value low nibble.
				blk.Id("_").Op("=").Id("e").Dot("EncodeTVHalfOctet").Call(
					jen.Lit(int(ieiByte>>4)),
					jen.Id(ref).Dot("Encode").Call(),
				)
			} else {
				// Full-byte TV: IEI + fixed-length value.
				if ieNeedsBitFieldByte(ieType) {
					blk.Id("_").Op("=").Id("e").Dot("EncodeTV").Call(
						jen.Lit(int(ieiByte)),
						jen.Index().Byte().Values(jen.Id(ref).Dot("Encode").Call()),
					)
				} else if ieType.GoType != "" {
					blk.Id("_").Op("=").Id("e").Dot("EncodeTV").Call(
						jen.Lit(int(ieiByte)), jen.Id(ref).Dot("Encode").Call())
				} else {
					blk.Id("_").Op("=").Id("e").Dot("EncodeTV").Call(
						jen.Lit(int(ieiByte)), jen.Id(ref).Dot("EncodeBytes").Call())
				}
			}
		case "T":
			blk.Id("_").Op("=").Id("e").Dot("EncodeT").Call(jen.Lit(int(ieiByte)))
		case "TLV":
			blk.Id("_").Op("=").Id("e").Dot("EncodeTLV").Call(
				jen.Lit(int(ieiByte)), jen.Id(ref).Dot("EncodeBytes").Call())
		case "TLV-E":
			if ieType.GoType != "" {
				blk.Id("_").Op("=").Id("e").Dot("EncodeTLVE").Call(
					jen.Lit(int(ieiByte)), jen.Id(ref).Dot("Encode").Call())
			} else {
				blk.Id("_").Op("=").Id("e").Dot("EncodeTLVE").Call(
					jen.Lit(int(ieiByte)), jen.Id(ref).Dot("EncodeBytes").Call())
			}
		default:
			blk.Comment("// unsupported optional format " + ie.Format + " for IE " + ie.Name)
		}
	})
}

// --- Decode ---

func (g *Generator) emitMessageDecode(f *jen.File, m schema.MessageDef, typeName string) {
	f.Func().Params(jen.Id("m").Op("*").Id(typeName)).
		Id("Decode").Params(jen.Id("payload").Index().Byte()).Error().
		BlockFunc(func(grp *jen.Group) {
			grp.Id("b").Op(":=").Add(g.qualRuntime("NewNASBuffer")).Call(jen.Id("payload"))
			grp.Id("var err error")
			grp.Id("_").Op("=").Id("err")

			// Mandatory IEs (with half-octet pairing)
			i := 0
			for i < len(m.IEs) {
				ie := m.IEs[i]
				if !ie.IsMandatory() {
					i++
					continue
				}
				if pi := pairIndex(m.IEs, i); pi != -1 {
					g.emitHalfOctetPairDecode(grp, m.Name, ie, m.IEs[pi])
					i = pi + 1
					continue
				}
				g.emitMandatoryDecode(grp, m.Name, ie)
				i++
			}

			// Optional IEs: loop until EOF, dispatch on IEI.
			g.emitOptionalDecodeLoop(grp, m)
			grp.Return(jen.Nil())
		})
}

func (g *Generator) emitHalfOctetPairDecode(grp *jen.Group, msgName string, low, high schema.IEEntry) {
	grp.BlockFunc(func(blk *jen.Group) {
		blk.Id("lo").Op(",").Id("hi").Op(",").Id("err").Op(":=").Id("b").Dot("ReadHalfOctetByte").Call()
		blk.If(jen.Id("err").Op("!=").Nil()).Block(
			jen.Return(g.errWrapMsg(msgName, low.Name+"/"+high.Name)),
		)
		blk.Id("m").Dot(GoName(low.Name)).Dot("Decode").Call(jen.Id("lo"))
		blk.Id("m").Dot(GoName(high.Name)).Dot("Decode").Call(jen.Id("hi"))
	})
}

func (g *Generator) emitMandatoryDecode(grp *jen.Group, msgName string, ie schema.IEEntry) {
	ref := "m." + GoName(ie.Name)
	ieType := g.Repo.IETypes[ie.TypeRef]

	switch ie.Format {
	case "V":
		if ieNeedsBitFieldByte(ieType) {
			grp.BlockFunc(func(blk *jen.Group) {
				blk.Id("v").Op(",").Id("err").Op(":=").Id("b").Dot("ReadByte").Call()
				blk.If(jen.Id("err").Op("!=").Nil()).Block(
					jen.Return(g.errWrapMsg(msgName, ie.Name)))
				blk.Id(ref).Dot("Decode").Call(jen.Id("v"))
			})
		} else if ieType.GoType != "" {
			grp.Comment("// mandatory V IE with runtime go_type; caller-provided bytes")
			grp.BlockFunc(func(blk *jen.Group) {
				blk.Id("_").Op("=").Id(ref) // placeholder — full decode requires fixed length in spec
			})
		} else {
			grp.BlockFunc(func(blk *jen.Group) {
				blk.Id("v").Op(",").Id("err").Op(":=").Id("b").Dot("ReadBytes").Call(jen.Lit(ieType.MinLength))
				blk.If(jen.Id("err").Op("!=").Nil()).Block(
					jen.Return(g.errWrapMsg(msgName, ie.Name)))
				blk.If(jen.Id("err").Op(":=").Id(ref).Dot("DecodeBytes").Call(jen.Id("v")).Op(";").
					Id("err").Op("!=").Nil()).Block(jen.Return(g.errWrapMsg(msgName, ie.Name)))
			})
		}
	case "LV":
		grp.BlockFunc(func(blk *jen.Group) {
			blk.Id("v").Op(",").Id("err").Op(":=").Id("b").Dot("DecodeLV").Call()
			blk.If(jen.Id("err").Op("!=").Nil()).Block(
				jen.Return(g.errWrapMsg(msgName, ie.Name)))
			if ieType.GoType == "MobileIdentity5GS" {
				blk.Id("mi").Op(",").Id("err").Op(":=").Add(g.qualRuntime("DecodeMobileIdentity5GS")).Call(jen.Id("v"))
				blk.If(jen.Id("err").Op("!=").Nil()).Block(jen.Return(g.errWrapMsg(msgName, ie.Name)))
				blk.Id(ref).Op("=").Id("mi")
			} else {
				blk.If(jen.Id("err").Op(":=").Id(ref).Dot("DecodeBytes").Call(jen.Id("v")).Op(";").
					Id("err").Op("!=").Nil()).Block(jen.Return(g.errWrapMsg(msgName, ie.Name)))
			}
		})
	case "LV-E":
		grp.BlockFunc(func(blk *jen.Group) {
			blk.Id("v").Op(",").Id("err").Op(":=").Id("b").Dot("DecodeLVE").Call()
			blk.If(jen.Id("err").Op("!=").Nil()).Block(
				jen.Return(g.errWrapMsg(msgName, ie.Name)))
			if ieType.GoType == "MobileIdentity5GS" {
				blk.Id("mi").Op(",").Id("err").Op(":=").Add(g.qualRuntime("DecodeMobileIdentity5GS")).Call(jen.Id("v"))
				blk.If(jen.Id("err").Op("!=").Nil()).Block(
					jen.Return(g.errWrapMsg(msgName, ie.Name)))
				blk.Id(ref).Op("=").Id("mi")
			} else {
				blk.If(jen.Id("err").Op(":=").Id(ref).Dot("DecodeBytes").Call(jen.Id("v")).Op(";").
					Id("err").Op("!=").Nil()).Block(jen.Return(g.errWrapMsg(msgName, ie.Name)))
			}
		})
	default:
		grp.Comment("// unsupported mandatory format " + ie.Format + " for IE " + ie.Name)
	}
}

func (g *Generator) emitOptionalDecodeLoop(grp *jen.Group, m schema.MessageDef) {
	grp.For(jen.Op("!").Id("b").Dot("EOF").Call()).BlockFunc(func(lp *jen.Group) {
		lp.Id("iei").Op(",").Id("err").Op(":=").Id("b").Dot("ReadByte").Call()
		lp.If(jen.Id("err").Op("!=").Nil()).Block(jen.Return(jen.Nil()))

		lp.Switch().BlockFunc(func(sw *jen.Group) {
			for _, ie := range m.IEs {
				if ie.IsMandatory() {
					continue
				}
				g.emitOptionalCase(sw, m.Name, ie)
			}
			sw.Default().BlockFunc(func(def *jen.Group) {
				// Skip unknown IE for forward compat.
				def.If(jen.Id("err").Op(":=").Id("b").Dot("SkipUnknownIE").Call(jen.Id("iei")).Op(";").
					Id("err").Op("!=").Nil()).Block(jen.Return(jen.Nil()))
			})
		})
	})
}

func (g *Generator) emitOptionalCase(sw *jen.Group, msgName string, ie schema.IEEntry) {
	ieType := g.Repo.IETypes[ie.TypeRef]
	ieiByte, half, _ := schema.ParseIEI(*ie.IEI)

	// For half-octet TV, the IEI is the high nibble and the value is the low nibble
	// of the SAME byte. Our loop has already consumed that byte as `iei`.
	// Match on the high nibble.
	var caseExpr *jen.Statement
	if half {
		caseExpr = jen.Id("iei").Op("&").Lit(0xF0).Op("==").Lit(int(ieiByte))
	} else {
		caseExpr = jen.Id("iei").Op("==").Lit(int(ieiByte))
	}

	sw.Case(caseExpr).BlockFunc(func(cs *jen.Group) {
		switch ie.Format {
		case "T":
			// presence-only
			cs.Id("flag").Op(":=").Id(GoName(ie.TypeRef)).Values()
			cs.Id("m").Dot(GoName(ie.Name)).Op("=").Op("&").Id("flag")
		case "TV":
			if half {
				// Decode from the same byte we already read: low nibble is the value.
				cs.Id("val").Op(":=").Id(GoName(ie.TypeRef)).Values()
				cs.Id("val").Dot("Decode").Call(jen.Id("iei").Op("&").Lit(0x0F))
				cs.Id("m").Dot(GoName(ie.Name)).Op("=").Op("&").Id("val")
			} else {
				// Full-byte TV: read ieType.MinLength more bytes (fixed).
				fixLen := ieType.MinLength
				if fixLen == 0 {
					fixLen = 1
				}
				cs.Id("v").Op(",").Id("err").Op(":=").Id("b").Dot("ReadBytes").Call(jen.Lit(fixLen))
				cs.If(jen.Id("err").Op("!=").Nil()).Block(
					jen.Return(g.errWrapMsg(msgName, ie.Name)))
				if ieNeedsBitFieldByte(ieType) {
					cs.Id("val").Op(":=").Id(GoName(ie.TypeRef)).Values()
					cs.Id("val").Dot("Decode").Call(jen.Id("v").Index(jen.Lit(0)))
					cs.Id("m").Dot(GoName(ie.Name)).Op("=").Op("&").Id("val")
				} else {
					cs.Id("val").Op(":=").Id(GoName(ie.TypeRef)).Values()
					cs.If(jen.Id("err").Op(":=").Id("val").Dot("DecodeBytes").Call(jen.Id("v")).Op(";").
						Id("err").Op("!=").Nil()).Block(jen.Return(g.errWrapMsg(msgName, ie.Name)))
					cs.Id("m").Dot(GoName(ie.Name)).Op("=").Op("&").Id("val")
				}
			}
		case "TLV":
			cs.Id("length").Op(",").Id("err").Op(":=").Id("b").Dot("ReadByte").Call()
			cs.If(jen.Id("err").Op("!=").Nil()).Block(
				jen.Return(g.errWrapMsg(msgName, ie.Name)))
			cs.Id("v").Op(",").Id("err").Op(":=").Id("b").Dot("ReadBytes").Call(jen.Int().Parens(jen.Id("length")))
			cs.If(jen.Id("err").Op("!=").Nil()).Block(
				jen.Return(g.errWrapMsg(msgName, ie.Name)))
			cs.Id("val").Op(":=").Id(GoName(ie.TypeRef)).Values()
			cs.If(jen.Id("err").Op(":=").Id("val").Dot("DecodeBytes").Call(jen.Id("v")).Op(";").
				Id("err").Op("!=").Nil()).Block(jen.Return(g.errWrapMsg(msgName, ie.Name)))
			cs.Id("m").Dot(GoName(ie.Name)).Op("=").Op("&").Id("val")
		case "TLV-E":
			cs.Id("length").Op(",").Id("err").Op(":=").Id("b").Dot("ReadUint16").Call()
			cs.If(jen.Id("err").Op("!=").Nil()).Block(
				jen.Return(g.errWrapMsg(msgName, ie.Name)))
			cs.Id("v").Op(",").Id("err").Op(":=").Id("b").Dot("ReadBytes").Call(jen.Int().Parens(jen.Id("length")))
			cs.If(jen.Id("err").Op("!=").Nil()).Block(
				jen.Return(g.errWrapMsg(msgName, ie.Name)))
			if ieType.GoType == "MobileIdentity5GS" {
				cs.Id("mi").Op(",").Id("err").Op(":=").Add(g.qualRuntime("DecodeMobileIdentity5GS")).Call(jen.Id("v"))
				cs.If(jen.Id("err").Op("!=").Nil()).Block(jen.Return(g.errWrapMsg(msgName, ie.Name)))
				cs.Id("m").Dot(GoName(ie.Name)).Op("=").Id("mi")
			} else {
				cs.Id("val").Op(":=").Id(GoName(ie.TypeRef)).Values()
				cs.If(jen.Id("err").Op(":=").Id("val").Dot("DecodeBytes").Call(jen.Id("v")).Op(";").
					Id("err").Op("!=").Nil()).Block(jen.Return(g.errWrapMsg(msgName, ie.Name)))
				cs.Id("m").Dot(GoName(ie.Name)).Op("=").Op("&").Id("val")
			}
		default:
			cs.Comment("// unsupported optional format " + ie.Format)
		}
	})
}
