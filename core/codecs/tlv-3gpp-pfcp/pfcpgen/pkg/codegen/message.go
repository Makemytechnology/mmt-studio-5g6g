// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
package codegen

import (
	"github.com/dave/jennifer/jen"
	"github.com/mmt/pfcpgen/pkg/schema"
)

// generateMessage emits the per-message struct + Encode / Decode.
//
// Struct layout:
//
//	type Foo struct {
//	    SEID           uint64   // only when HasSEID
//	    SequenceNumber uint32   // 24-bit sequence number
//	    Priority       uint8    // 4-bit priority (optional)
//	    ... one field per IE (pointer for optional, slice for multiple) ...
//	}
//
// Encode: builds a PFCP header + concatenated IE TLVs.
// Decode: takes the payload (after ParseHeader) and walks IEs dispatching on
// type code. Unknown IEs are skipped for forward compatibility.
func (g *Generator) generateMessage(f *jen.File, m schema.MessageDef) {
	name := GoName(m.Name)

	f.Const().Id("MessageType" + name).Op("=").Lit(int(m.MessageType))

	f.Commentf("// %s — %s", name, m.Description)
	f.Type().Id(name).StructFunc(func(grp *jen.Group) {
		if m.HasSEID {
			grp.Id("SEID").Uint64().Commentf("session endpoint id (header)")
		}
		grp.Id("SequenceNumber").Uint32().Commentf("24-bit")
		grp.Id("Priority").Uint8().Commentf("4-bit, if MP flag set")
		for _, ie := range m.IEs {
			g.emitMemberField(grp, ie)
		}
	})

	// Encode()
	f.Func().Params(jen.Id("m").Op("*").Id(name)).Id("Encode").Params().
		Params(jen.Index().Byte(), jen.Error()).
		BlockFunc(func(grp *jen.Group) {
			if len(m.IEs) > 0 {
				grp.Id("t").Op(":=").Id("m")
				grp.Id("_").Op("=").Id("t")
			}
			grp.Id("e").Op(":=").Add(g.qualRuntime("NewEncoder")).Call()
			for _, ie := range m.IEs {
				g.emitMemberEncode(grp, ie)
			}
			grp.Id("payload").Op(":=").Id("e").Dot("Bytes").Call()
			grp.Id("h").Op(":=").Op("&").Add(g.qualRuntime("Header")).Values(jen.Dict{
				jen.Id("Version"):        jen.Lit(1),
				jen.Id("HasSEID"):        jen.Lit(m.HasSEID),
				jen.Id("MessageType"):    jen.Lit(int(m.MessageType)),
				jen.Id("SequenceNumber"): jen.Id("m").Dot("SequenceNumber"),
				jen.Id("Priority"):       jen.Id("m").Dot("Priority"),
			})
			if m.HasSEID {
				grp.Id("h").Dot("SEID").Op("=").Id("m").Dot("SEID")
			}
			// Length is the number of bytes after the 4-octet basic header.
			// That equals (header - 4) + payload.
			grp.Id("h").Dot("Length").Op("=").Uint16().Parens(
				jen.Id("h").Dot("HeaderSize").Call().Op("-").Lit(4).Op("+").Len(jen.Id("payload"))).
				Commentf("bytes after octets 1-4")
			grp.Id("headerBytes").Op(":=").Id("h").Dot("Encode").Call()
			grp.Return(jen.Append(jen.Id("headerBytes"), jen.Id("payload").Op("...")), jen.Nil())
		})

	// Decode(payload []byte) — payload begins at the first IE.
	// The dispatch-switch helper writes into a symbol named `t`, so we alias
	// `t := m` to reuse the same emitter between grouped IEs and messages.
	f.Func().Params(jen.Id("m").Op("*").Id(name)).Id("Decode").
		Params(jen.Id("payload").Index().Byte()).Error().
		BlockFunc(func(grp *jen.Group) {
			if len(m.IEs) == 0 {
				grp.Return(jen.Nil())
				return
			}
			grp.Id("t").Op(":=").Id("m")
			grp.Id("_").Op("=").Id("t")
			grp.Id("b").Op(":=").Add(g.qualRuntime("NewBuffer")).Call(jen.Id("payload"))
			grp.Return(jen.Id("b").Dot("ForEachIE").Call(
				jen.Func().Params(jen.Id("ie").Op("*").Add(g.qualRuntime("DecodedIE"))).Error().
					BlockFunc(func(fn *jen.Group) {
						g.emitMemberDispatchSwitch(fn, m.IEs, m.Name)
					}),
			))
		})

	// Also: the message's Encode emitter uses emitMemberEncode which also
	// references `t` — alias inside Encode too.
}
